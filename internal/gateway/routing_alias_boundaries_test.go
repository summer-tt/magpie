package gateway

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/filememo"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

func routingAliasBinding(key string, active bool) map[string]any {
	return map[string]any{"ownerId": provider.KeyID(key), "keyId": provider.KeyID(key), "active": active}
}
func TestRoutingAliasAllKeysLeaveOrdinaryPool(t *testing.T) {
	v, h, update := routingAliasFixture(t)
	update(map[string]any{"work": routingAliasBinding("k-work", true), "personal": routingAliasBinding("k-ordinary", true)})
	w := routingAliasSend(h, "alias-relay/m", "")
	got := v.take()
	if w.Code != 200 || !slices.Equal(got, []routingAliasCall{{"k-spare", "m"}}) {
		t.Errorf("all alias ordinary route: %d %s calls=%v; only configured ordinary spare may answer", w.Code, w.Body.String(), got)
	}
}
func TestRoutingAliasHeaderOnlyNarrows(t *testing.T) {
	for _, tc := range []struct {
		name, model, account string
		allowed              bool
	}{
		{"ordinary-cannot-select-isolated", "gpt-daybreak-blue-latest", "spare@example.com", false},
		{"qualified-conflict", "gpt-daybreak-blue-latest@work", "me@example.com", false},
		{"qualified-same-owner", "gpt-daybreak-blue-latest@work", "spare@example.com", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			codexSignedIn(t, "spare@example.com")
			d := daybreak(t)
			h := New().Front()
			send := func(model, account string) *httptest.ResponseRecorder {
				r := httptest.NewRequest("POST", "http://localhost/v1/responses", strings.NewReader(`{"model":"codex/`+model+`","input":"hello","stream":true}`))
				r.RemoteAddr = "127.0.0.1:12345"
				r.Header.Set(AccountHeader, account)
				w := httptest.NewRecorder()
				h.ServeHTTP(w, r)
				return w
			}
			if w := send("gpt-daybreak-blue-latest", "spare@example.com"); w.Code != 200 || d.asked() != 1 {
				t.Fatalf("baseline supported subscription header: %d %s calls=%d", w.Code, w.Body.String(), d.asked())
			}
			owner := provider.LoginID("codex", "spare@example.com")
			putProvider(t, map[string]any{"id": "codex", "accountAliases": map[string]any{"work": map[string]any{"accountId": owner, "active": true}}})
			filememo.Forget()
			catalog.Touched()
			before := d.asked()
			w := send(tc.model, tc.account)
			if tc.allowed {
				if w.Code != 200 || d.asked() != before+1 {
					t.Errorf("same-owner qualified header: %d %s calls=%d", w.Code, w.Body.String(), d.asked()-before)
				}
				if d.asked() > before && modelOf(d.last(t)) != "gpt-daybreak-blue-latest" {
					t.Errorf("qualified header sent model %q upstream; want plain gpt-daybreak-blue-latest", modelOf(d.last(t)))
				}
			} else {
				if w.Code < 400 {
					t.Errorf("conflicting header succeeded: %d %s", w.Code, w.Body.String())
				}
				if d.asked() != before {
					t.Errorf("conflicting header consumed subscription: before=%d after=%d", before, d.asked())
				}
			}
		})
	}
}
func TestRoutingAliasMalformedReferencesFailLocally(t *testing.T) {
	for _, model := range []string{"m@", "m@bad!", "m@_work", "m@1work"} {
		t.Run(model, func(t *testing.T) {
			v, h, _ := routingAliasFixture(t)
			w := routingAliasSend(h, "alias-relay/"+model, "")
			if w.Code != 400 {
				t.Errorf("malformed alias: %d %s; want 400", w.Code, w.Body.String())
			}
			if got := v.take(); len(got) != 0 {
				t.Errorf("malformed alias consumed upstream: %v", got)
			}
		})
	}
}
func TestRoutingAliasGroupOnlyUsesExplicitOwners(t *testing.T) {
	for _, retired := range []bool{false, true} {
		t.Run(map[bool]string{false: "target-fails", true: "target-retired"}[retired], func(t *testing.T) {
			v, h, update := routingAliasFixture(t)
			if err := provider.SaveGroup(provider.Group{ID: "alias-owners", Name: "Alias owners", Members: []string{"alias-relay/m", "alias-spare/m"}, Routing: provider.Ordered}); err != nil {
				t.Fatal(err)
			}
			if w := routingAliasSend(h, "group/alias-owners", ""); w.Code != 200 {
				t.Fatalf("baseline explicit plain group: %d %s", w.Code, w.Body.String())
			}
			v.take()
			update(map[string]any{"work": routingAliasBinding("k-work", !retired), "backup": routingAliasBinding("k-ordinary", true)})
			if err := provider.SaveGroup(provider.Group{ID: "alias-owners", Name: "Alias owners", Members: []string{"alias-relay/m@work", "alias-relay/m@backup"}, Routing: provider.Ordered}); err != nil {
				t.Fatal(err)
			}
			v.mu.Lock()
			v.refused["k-work"] = 429
			v.mu.Unlock()
			w := routingAliasSend(h, "group/alias-owners", "")
			got := v.take()
			if w.Code != 200 {
				t.Errorf("explicit backup group: %d %s", w.Code, w.Body.String())
			}
			if !slices.Contains(got, routingAliasCall{"k-ordinary", "m"}) {
				t.Errorf("group did not reach explicit backup with plain model: %v", got)
			}
			for _, c := range got {
				if c.Key == "k-spare" || c.Model != "m" || retired && c.Key == "k-work" {
					t.Errorf("group consumed nonmember/retired owner or suffixed model: %v", got)
				}
			}
		})
	}
}
func TestRoutingAliasGatewayModelsPermissions(t *testing.T) {
	for _, tc := range []struct {
		name  string
		allow []string
		model string
		want  int
	}{
		{"base-grants-qualified", []string{"alias-relay/m"}, "m@work", 200},
		{"wildcard-grants-qualified", []string{"alias-relay/*"}, "m@work", 200},
		{"exact-grants-only-self", []string{"alias-relay/m@work"}, "m@work", 200},
		{"exact-denies-base", []string{"alias-relay/m@work"}, "m", 403},
		{"exact-denies-other-alias", []string{"alias-relay/m@other"}, "m@work", 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v, h, _ := routingAliasFixture(t)
			keys, secrets := newCaller(t, "Alias key")
			if _, err := access.Update("models-key", access.Change{Key: keys[0].ID, Models: tc.allow}); err != nil {
				t.Fatal(err)
			}
			r := httptest.NewRequest("POST", "http://localhost/v1/chat/completions", strings.NewReader(`{"model":"alias-relay/`+tc.model+`","messages":[{"role":"user","content":"hello"}]}`))
			r.RemoteAddr = "127.0.0.1:12345"
			r.Header.Set("Authorization", "Bearer "+secrets[0])
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tc.want {
				t.Errorf("model permission: %d %s; want %d", w.Code, w.Body.String(), tc.want)
			}
			got := v.take()
			if tc.want == 200 {
				if !slices.Equal(got, []routingAliasCall{{"k-work", "m"}}) {
					t.Errorf("authorized alias upstream=%v", got)
				}
			} else if len(got) != 0 {
				t.Errorf("denied model consumed upstream=%v", got)
			}
		})
	}
}
func TestRoutingAliasOriginalConstraintPrecedesTitleRewrite(t *testing.T) {
	for _, alias := range []string{"work", "unknown"} {
		t.Run(alias, func(t *testing.T) {
			v, h, _ := routingAliasFixture(t)
			st := settings.Load()
			st.CodexTitles = "alias-spare/m"
			if err := settings.Save(st); err != nil {
				t.Fatal(err)
			}
			r := httptest.NewRequest("POST", "http://localhost/v1/responses", strings.NewReader(`{"model":"alias-relay/m@`+alias+`","input":"Generate a concise title.","text":{"format":{"type":"json_schema","name":"codex_output_schema","strict":true,"schema":{"type":"object","properties":{"title":{"type":"string"}},"required":["title"],"additionalProperties":false}}}}`))
			r.RemoteAddr = "127.0.0.1:12345"
			r.Header.Set("x-openai-subagent", "thread_title")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			got := v.take()
			if alias == "unknown" {
				if w.Code != 404 {
					t.Errorf("unknown original alias before title rewrite: %d %s", w.Code, w.Body.String())
				}
				if len(got) != 0 {
					t.Errorf("unknown original alias consumed title target=%v", got)
				}
			} else {
				for _, c := range got {
					if c.Key != "k-work" || c.Model != "m" {
						t.Errorf("title rewrite escaped original owner: %v", got)
					}
				}
				if len(got) == 0 && w.Code < 400 {
					t.Errorf("known original alias silently answered without bound owner: %d %s", w.Code, w.Body.String())
				}
			}
		})
	}
}

func TestRoutingAliasUnreadableProvidersNeverRestoreDiscoveredAccount(t *testing.T) {
	for _, state := range []string{"malformed-json", "registry-type", "unreadable-directory", "old-backup"} {
		t.Run(state, func(t *testing.T) {
			codexSignedIn(t, "spare@example.com")
			d := daybreak(t)
			h := New().Front()
			request := func() *httptest.ResponseRecorder {
				r := httptest.NewRequest("POST", "http://localhost/v1/responses", strings.NewReader(`{"model":"codex/gpt-daybreak-blue-latest","input":"hello","stream":true}`))
				r.RemoteAddr = "127.0.0.1:12345"
				w := httptest.NewRecorder()
				h.ServeHTTP(w, r)
				return w
			}
			if w := request(); w.Code != 200 || d.asked() == 0 {
				t.Fatalf("baseline discovered subscription: %d %s calls=%d", w.Code, w.Body.String(), d.asked())
			}
			before := d.asked()
			path := provider.Path()
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			if state == "unreadable-directory" {
				if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
					t.Fatal(err)
				}
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			} else {
				body := []byte(`{"providers":[`)
				if state == "registry-type" {
					body = []byte(`{"providers":[{"id":"codex","accountAliases":42}]}`)
				}
				if state == "old-backup" {
					if err := os.WriteFile(path+".bak", []byte(`{"providers":[{"id":"codex","models":["gpt-daybreak-blue-latest"]}]}`), 0600); err != nil {
						t.Fatal(err)
					}
				}
				if err := os.WriteFile(path, body, 0600); err != nil {
					t.Fatal(err)
				}
			}
			filememo.Forget()
			catalog.Touched()
			for _, temperature := range []string{"hot", "new-server"} {
				if temperature == "new-server" {
					h = New().Front()
				}
				w := request()
				if w.Code < 400 {
					t.Errorf("%s unknown providers state served request: %d %s", temperature, w.Code, w.Body.String())
				}
				if d.asked() != before {
					t.Errorf("%s unknown isolation consumed discovered subscription: before=%d after=%d", temperature, before, d.asked())
				}
			}
		})
	}
}
