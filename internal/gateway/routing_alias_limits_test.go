package gateway

import (
	"encoding/json"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/filememo"
	"github.com/yetone/magpie/internal/provider"
)

func TestRoutingAliasAccountsOnlyCatalogAndIntersection(t *testing.T) {
	for _, tc := range []struct {
		name, key string
		models    []string
		allowed   bool
	}{{"accounts-only-deny", "k-ordinary", nil, false}, {"accounts-only-allow", "k-work", nil, true}, {"base-model-denied-account", "k-ordinary", []string{"alias-relay/m"}, false}, {"exact-model-allowed-account", "k-work", []string{"alias-relay/m@work"}, true}} {
		t.Run(tc.name, func(t *testing.T) {
			v, h, _ := routingAliasFixture(t)
			ks, secrets := newCaller(t, "Scoped alias")
			call := func(method, path, body string) *httptest.ResponseRecorder {
				r := httptest.NewRequest(method, "http://localhost"+path, strings.NewReader(body))
				r.RemoteAddr = "127.0.0.1:1234"
				r.Header.Set("Authorization", "Bearer "+secrets[0])
				w := httptest.NewRecorder()
				h.ServeHTTP(w, r)
				return w
			}
			body := `{"model":"alias-relay/m@work","messages":[{"role":"user","content":"hi"}]}`
			if w := call("POST", "/v1/chat/completions", body); w.Code != 200 {
				t.Fatalf("baseline caller qualified: %d %s", w.Code, w.Body.String())
			}
			if got := v.take(); !slices.Equal(got, []routingAliasCall{{"k-work", "m"}}) {
				t.Fatalf("baseline caller target=%v", got)
			}
			if _, err := access.Update("accounts-key", access.Change{Key: ks[0].ID, Accounts: []string{"alias-relay/" + provider.KeyID(tc.key)}}); err != nil {
				t.Fatal(err)
			}
			if tc.models != nil {
				if _, err := access.Update("models-key", access.Change{Key: ks[0].ID, Models: tc.models}); err != nil {
					t.Fatal(err)
				}
			}
			w := call("GET", "/v1/models", "")
			var list struct {
				Data []struct {
					ID string `json:"id"`
				} `json:"data"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil || w.Code != 200 {
				t.Fatalf("catalog: %d decode=%v", w.Code, err)
			}
			found := false
			for _, e := range list.Data {
				found = found || e.ID == "alias-relay/m@work"
			}
			if found != tc.allowed {
				t.Errorf("accounts/model restricted catalog qualified visible=%v want=%v", found, tc.allowed)
			}
			w = call("POST", "/v1/chat/completions", body)
			got := v.take()
			if tc.allowed {
				if w.Code != 200 || !slices.Equal(got, []routingAliasCall{{"k-work", "m"}}) {
					t.Errorf("allowed intersection status=%d calls=%v", w.Code, got)
				}
			} else if w.Code != 403 || len(got) != 0 {
				t.Errorf("denied intersection status=%d calls=%v; want403 zero calls", w.Code, got)
			}
		})
	}
}
func TestRoutingAliasSingleKeyEmptyPoolAndExplicitModelRestriction(t *testing.T) {
	for _, mode := range []string{"single-key", "model-list", "rest"} {
		t.Run(mode, func(t *testing.T) {
			v, h, update := routingAliasFixture(t)
			if w := routingAliasSend(h, "alias-relay/m@work", ""); w.Code != 200 {
				t.Fatalf("baseline qualified: %d %s", w.Code, w.Body.String())
			}
			v.take()
			p, err := provider.Find("alias-relay")
			if err != nil {
				t.Fatal(err)
			}
			p.Fallback = nil
			if mode == "single-key" {
				p.Keys = nil
				p.Key = "k-work"
			}
			p.Models = append(p.Models, "other")
			if err = provider.Save(*p); err != nil {
				t.Fatal(err)
			}
			update(map[string]any{"work": routingAliasBinding("k-work", true)})
			switch mode {
			case "single-key":
				w := routingAliasSend(h, "alias-relay/m", "")
				if w.Code != 404 {
					t.Errorf("ordinary single isolated key status=%d want404", w.Code)
				}
				if got := v.take(); len(got) != 0 {
					t.Errorf("empty ordinary pool restored primary=%v", got)
				}
			case "model-list":
				if err := provider.SetAccountModels(p.ID, provider.KeyID("k-work"), []string{"other"}); err != nil {
					t.Fatal(err)
				}
				w := routingAliasSend(h, "alias-relay/m@work", "")
				if w.Code < 400 {
					t.Errorf("qualified disallowed account model status=%d", w.Code)
				}
				if got := v.take(); len(got) != 0 {
					t.Errorf("account model deny consumed upstream=%v", got)
				}
			case "rest":
				v.mu.Lock()
				v.refused["k-work"] = 429
				v.mu.Unlock()
				w := routingAliasSend(h, "alias-relay/m@work", "")
				if w.Code != 429 {
					t.Errorf("qualified quota status=%d want429", w.Code)
				}
				v.take()
				w = routingAliasSend(h, "alias-relay/m@work", "")
				if w.Code != 429 {
					t.Errorf("qualified resting status=%d want429", w.Code)
				}
				for _, c := range v.take() {
					if c.Key != "k-work" {
						t.Errorf("rest recovered ordinary owner=%v", c)
					}
				}
			}
		})
	}
}
func TestRoutingAliasSingleSubscriptionEmptyPool(t *testing.T) {
	codexSignedIn(t)
	d := daybreak(t)
	h := New().Front()
	send := func() *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "http://localhost/v1/responses", strings.NewReader(`{"model":"codex/gpt-daybreak-blue-latest","input":"hello","stream":true}`))
		r.RemoteAddr = "127.0.0.1:1234"
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if w := send(); w.Code != 200 || d.asked() == 0 {
		t.Fatalf("baseline subscription status=%d calls=%d", w.Code, d.asked())
	}
	before := d.asked()
	putProvider(t, map[string]any{"id": "codex", "accountAliases": map[string]any{"work": map[string]any{"accountId": provider.LoginID("codex", "me@example.com"), "active": true}}})
	filememo.Forget()
	catalog.Touched()
	if w := send(); w.Code != 404 {
		t.Errorf("single isolated subscription ordinary status=%d want404", w.Code)
	}
	if d.asked() != before {
		t.Errorf("ordinary pool consumed sole isolated subscription: before=%d after=%d", before, d.asked())
	}
}

func TestRoutingAliasOldSessionAffinityCannotRecoverIsolatedOwner(t *testing.T) {
	v, h, update := routingAliasFixture(t)
	update(map[string]any{})
	if err := provider.SetAffinity("alias-relay", "session"); err != nil {
		t.Fatal(err)
	}
	send := func() *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "http://localhost/v1/chat/completions", strings.NewReader(`{"model":"alias-relay/m","messages":[{"role":"user","content":"hi"}]}`))
		r.RemoteAddr = "127.0.0.1:1234"
		r.Header.Set("x-session-id", "old-alias-session")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if w := send(); w.Code != 200 {
		t.Fatalf("baseline session status=%d", w.Code)
	}
	// which legal owner the session answered on: Stays: Session spreads a
	// new session over the provider's keys (06d36d41), so it is whoever
	// answered, not a fixed first key
	base := v.take()
	if len(base) != 1 || base[0].Model != "m" || base[0].Key != "k-ordinary" && base[0].Key != "k-work" {
		t.Fatalf("baseline session target=%v; want one of the provider's own keys answering", base)
	}
	owner, other := base[0].Key, "k-work"
	if owner == "k-work" {
		other = "k-ordinary"
	}
	// the owner the session answered on is isolated: its old affinity
	// must not bring it back, and the key left in the ordinary pool answers
	update(map[string]any{"personal": routingAliasBinding(owner, true)})
	if w := send(); w.Code != 200 {
		t.Errorf("session after isolation=%d", w.Code)
	}
	got := v.take()
	if slices.ContainsFunc(got, func(c routingAliasCall) bool { return c.Key == owner }) {
		t.Errorf("old session recovered isolated owner %s: %v", owner, got)
	}
	if !slices.Equal(got, []routingAliasCall{{other, "m"}}) {
		t.Errorf("session after isolating %s: %v; want the remaining legal owner %s", owner, got, other)
	}
}
