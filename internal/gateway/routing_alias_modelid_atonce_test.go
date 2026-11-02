package gateway

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/filememo"
	"github.com/yetone/magpie/internal/provider"
)

// aliasCombCall is one request the fake vendor was asked, by key and model.
type aliasCombCall struct{ Key, Model string }

// aliasCombVendor answers each request by its key, recording the key and
// the model's own id it was asked for, and refusing the keys in fail.
type aliasCombVendor struct {
	mu    sync.Mutex
	calls []aliasCombCall
	fail  map[string]bool
}

func (v *aliasCombVendor) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if strings.HasSuffix(r.URL.Path, "/models") {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"data":[{"id":"m-own"}]}`)
		return
	}
	var body struct {
		Model string `json:"model"`
	}
	json.NewDecoder(r.Body).Decode(&body)
	key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	v.mu.Lock()
	v.calls = append(v.calls, aliasCombCall{key, body.Model})
	fail := v.fail[key]
	v.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if fail {
		w.WriteHeader(http.StatusTooManyRequests)
		io.WriteString(w, `{"error":{"message":"rate limited"}}`)
		return
	}
	fmt.Fprintf(w, `{"id":"x","object":"chat.completion","model":%q,"choices":[{"index":0,"message":{"role":"assistant","content":"from %s"},"finish_reason":"stop"}]}`, body.Model, key)
}

func (v *aliasCombVendor) take() []aliasCombCall {
	v.mu.Lock()
	defer v.mu.Unlock()
	out := slices.Clone(v.calls)
	v.calls = nil
	return out
}

// aliasCombRegistry writes a key routing-alias registry into providers.json
// by its raw JSON fields (the frozen data contract), so the tests depend on
// no alias API: filememo and the catalog are dropped afterwards as a save
// would.
func aliasCombRegistry(t *testing.T, id string, aliases map[string]string) {
	t.Helper()
	b, err := os.ReadFile(provider.Path())
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err = json.Unmarshal(b, &cfg); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, raw := range cfg["providers"].([]any) {
		row := raw.(map[string]any)
		if row["id"] != id {
			continue
		}
		reg := map[string]any{}
		for alias, key := range aliases {
			reg[alias] = map[string]any{"ownerId": provider.KeyID(key), "keyId": provider.KeyID(key), "active": true}
		}
		row["keyAliases"] = reg
		found = true
	}
	if !found {
		t.Fatalf("%s is not in %s", id, provider.Path())
	}
	if b, err = json.Marshal(cfg); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(provider.Path(), b, 0600); err != nil {
		t.Fatal(err)
	}
	filememo.Forget()
	catalog.Touched()
}

// aliasCombAsk is the vendor's calls cleared, for one request's own.
func aliasCombAsk(v *aliasCombVendor) []aliasCombCall { return v.take() }

// The models a user lists by ids of their own (provider.SetModelID) and
// account routing aliases together: the listed id is the reference agents
// and gateway keys see, the provider's own id is what the upstream is
// asked for, and a key's grant resolves through both ids. A base grant of
// either id allows the base and every qualified variant of it; a grant of
// one exact qualified reference allows that variant alone, not the base
// nor another owner's — and every qualified request still leaves for the
// provider's own model id, on the owner's key alone.
func TestRoutingAliasModelIDPermissions(t *testing.T) {
	fresh(t)
	v := &aliasCombVendor{fail: map[string]bool{}}
	up := httptest.NewServer(v)
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{ID: "alias-id", Name: "Alias ID", Chat: up.URL + "/v1", Key: "k-ordinary", Keys: []provider.KeyAccount{{Key: "k-work"}, {Key: "k-team"}}}); err != nil {
		t.Fatal(err)
	}
	if err := catalog.SaveLive("alias-id", up.URL+"/v1", []catalog.Model{{ID: "m-own"}}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := provider.SetModelID("alias-id/m-own", "m"); err != nil {
		t.Fatal(err)
	}
	s := New()
	send := func(secret, model string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"`+model+`","messages":[{"role":"user","content":"hi"}]}`))
		if secret != "" {
			r.Header.Set("Authorization", "Bearer "+secret)
		}
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		return w
	}
	// legal baseline: the user's listed id, asked upstream by the
	// provider's own id, before any alias exists
	if w := send("", "alias-id/m"); w.Code != 200 {
		t.Fatalf("baseline listed id: %d %s", w.Code, w.Body.String())
	}
	if got := aliasCombAsk(v); !slices.Equal(got, []aliasCombCall{{"k-ordinary", "m-own"}}) {
		t.Fatalf("baseline upstream = %v; want the plain key and the provider's own id", got)
	}
	aliasCombRegistry(t, "alias-id", map[string]string{"work": "k-work", "team": "k-team"})
	// the ordinary pool is the key without an alias; each qualified
	// reference goes to its owner alone, still by the own id upstream
	for _, c := range []struct{ ref, key string }{{"alias-id/m", "k-ordinary"}, {"alias-id/m@work", "k-work"}, {"alias-id/m@team", "k-team"}} {
		if w := send("", c.ref); w.Code != 200 {
			t.Fatalf("unrestricted %s: %d %s", c.ref, w.Code, w.Body.String())
		}
		if got := aliasCombAsk(v); !slices.Equal(got, []aliasCombCall{{c.key, "m-own"}}) {
			t.Fatalf("unrestricted %s upstream = %v; want %s asked for the own id", c.ref, got, c.key)
		}
	}
	keys, secrets := newCaller(t, "Held")
	hold := func(models ...string) {
		t.Helper()
		if _, err := access.Update("models-key", access.Change{Key: keys[0].ID, Models: models}); err != nil {
			t.Fatal(err)
		}
	}
	// a base grant by the listed id allows the base and every qualified
	// variant of it
	hold("alias-id/m")
	for _, c := range []struct{ ref, key string }{{"alias-id/m", "k-ordinary"}, {"alias-id/m@work", "k-work"}, {"alias-id/m@team", "k-team"}} {
		if w := send(secrets[0], c.ref); w.Code != 200 {
			t.Fatalf("base grant %s: %d %s", c.ref, w.Code, w.Body.String())
		}
		if got := aliasCombAsk(v); !slices.Equal(got, []aliasCombCall{{c.key, "m-own"}}) {
			t.Fatalf("base grant %s upstream = %v", c.ref, got)
		}
	}
	// a grant of one exact qualified reference: that owner alone — not
	// the base, not the other owner's variant
	hold("alias-id/m@work")
	if w := send(secrets[0], "alias-id/m@work"); w.Code != 200 {
		t.Fatalf("exact grant base refused its own variant: %d %s", w.Code, w.Body.String())
	}
	if got := aliasCombAsk(v); !slices.Equal(got, []aliasCombCall{{"k-work", "m-own"}}) {
		t.Fatalf("exact grant upstream = %v", got)
	}
	for _, ref := range []string{"alias-id/m", "alias-id/m@team"} {
		if w := send(secrets[0], ref); w.Code != 403 {
			t.Errorf("exact grant %s: %d %s; want 403", ref, w.Code, w.Body.String())
		}
		if got := aliasCombAsk(v); len(got) != 0 {
			t.Errorf("exact grant %s consumed upstream: %v", ref, got)
		}
	}
	// a base grant written by the provider's own id (before the id the
	// user gave the model): the base and the qualified variant both work
	hold("alias-id/m-own")
	for _, c := range []struct{ ref, key string }{{"alias-id/m", "k-ordinary"}, {"alias-id/m@work", "k-work"}} {
		if w := send(secrets[0], c.ref); w.Code != 200 {
			t.Errorf("own-id grant %s: %d %s", c.ref, w.Code, w.Body.String())
		}
		if got := aliasCombAsk(v); !slices.Equal(got, []aliasCombCall{{c.key, "m-own"}}) {
			t.Errorf("own-id grant %s upstream = %v", c.ref, got)
		}
	}
}

// aliasCombOnceAsked is the atOnceVendor's keys asked so far, cleared.
func aliasCombOnceAsked(v *atOnceVendor) []string {
	v.mu.Lock()
	defer v.mu.Unlock()
	out := slices.Clone(v.asked)
	v.asked = nil
	return out
}

// The plain keys an AtOnce limit is about are the keys without an alias:
// an aliased key leaves the ordinary pool before the limit is placed, so
// it neither takes one of the first N seats nor comes back as a reserve;
// and a qualified request goes to its owner alone, switching to no other
// key even with seats free.
func TestRoutingAliasAtOnceOrdinaryPool(t *testing.T) {
	fresh(t)
	v := &atOnceVendor{fail: map[string]bool{"k1": true}}
	up := httptest.NewServer(v)
	t.Cleanup(up.Close)
	two := 2
	if err := provider.Save(provider.Provider{ID: "alias-once", Name: "Alias once", Chat: up.URL + "/v1", Key: "k1", Keys: []provider.KeyAccount{{Key: "k2"}, {Key: "k3"}},
		Models: []string{"m"}, Routing: provider.Ordered, MaxAccounts: &two}); err != nil {
		t.Fatal(err)
	}
	s := New()
	// legal baseline: AtOnce 2 keeps the first two in the user's order,
	// so the second answers when the first refuses
	if code, body := postAs(t, s, "", `{"model":"alias-once/m","messages":[{"role":"user","content":"hi"}]}`); code != 200 || !strings.Contains(body, "from k2") {
		t.Fatalf("baseline second seat: %d %s; want k2 answering", code, body)
	}
	aliasCombOnceAsked(v)
	v.mu.Lock()
	v.fail = nil
	v.mu.Unlock()
	restingUntil.Lock()
	restingUntil.m = map[string]time.Time{}
	restingUntil.Unlock()
	aliasCombRegistry(t, "alias-once", map[string]string{"work": "k2"})
	// the ordinary pool is the two plain keys, both within AtOnce: neither
	// held back as a reserve for the aliased key's account
	p, err := provider.Find("alias-once")
	if err != nil {
		t.Fatal(err)
	}
	cs, _ := (&Server{}).plan(*p, "m", provider.Chat)
	var pooled, reserved []string
	for _, c := range cs {
		pooled = append(pooled, c.p.Key)
		if c.reserve > 0 {
			reserved = append(reserved, c.p.Key)
		}
	}
	slices.Sort(pooled)
	if !slices.Equal(pooled, []string{"k1", "k3"}) {
		t.Errorf("ordinary pool = %v; want the two keys without an alias", pooled)
	}
	if len(reserved) != 0 {
		t.Errorf("plain keys held back as reserves = %v; the aliased key must not take an AtOnce seat", reserved)
	}
	// the aliased key takes no ordinary request, even when every plain key
	// refuses: those two are the whole pool
	v.mu.Lock()
	v.fail = map[string]bool{"k1": true, "k3": true}
	v.mu.Unlock()
	if code, body := postAs(t, s, "", `{"model":"alias-once/m","messages":[{"role":"user","content":"hi"}]}`); code != 429 {
		t.Errorf("every plain key refusing: %d %s; want the refusal, not the aliased key", code, body)
	}
	if asked := aliasCombOnceAsked(v); slices.Contains(asked, "k2") {
		t.Errorf("an ordinary request reached the aliased key: %v", asked)
	}
	v.mu.Lock()
	v.fail = nil
	v.mu.Unlock()
	restingUntil.Lock()
	restingUntil.m = map[string]time.Time{}
	restingUntil.Unlock()
	// a qualified request goes to its owner alone
	if code, body := postAs(t, s, "", `{"model":"alias-once/m@work","messages":[{"role":"user","content":"hi"}]}`); code != 200 || !strings.Contains(body, "from k2") {
		t.Fatalf("qualified request: %d %s; want k2 answering", code, body)
	}
	aliasCombOnceAsked(v)
	// and when the owner refuses, no other key is switched in
	v.mu.Lock()
	v.fail = map[string]bool{"k2": true}
	v.mu.Unlock()
	if code, body := postAs(t, s, "", `{"model":"alias-once/m@work","messages":[{"role":"user","content":"hi"}]}`); code != 429 {
		t.Errorf("owner refusing: %d %s; want its refusal", code, body)
	}
	asked := aliasCombOnceAsked(v)
	if !slices.Contains(asked, "k2") {
		t.Errorf("the owner was not asked: %v", asked)
	}
	for _, key := range []string{"k1", "k3"} {
		if slices.Contains(asked, key) {
			t.Errorf("a qualified request switched to %s: %v", key, asked)
		}
	}
}
