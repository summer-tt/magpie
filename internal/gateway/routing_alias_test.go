package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/filememo"
	"github.com/yetone/magpie/internal/provider"
)

type routingAliasCall struct{ Key, Model string }
type routingAliasVendor struct {
	mu      sync.Mutex
	calls   []routingAliasCall
	refused map[string]int
}

func (v *routingAliasVendor) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if strings.HasSuffix(r.URL.Path, "/models") {
		io.WriteString(w, `{"data":[{"id":"m"}]}`)
		return
	}
	var body struct {
		Model  string `json:"model"`
		Stream bool   `json:"stream"`
	}
	json.NewDecoder(r.Body).Decode(&body)
	key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	v.mu.Lock()
	v.calls = append(v.calls, routingAliasCall{key, body.Model})
	status := v.refused[key]
	v.mu.Unlock()
	if status != 0 {
		w.WriteHeader(status)
		io.WriteString(w, `{"error":{"message":"target unavailable"}}`)
		return
	}
	if body.Stream {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"id\":\"answer\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Fixture title\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
		return
	}
	io.WriteString(w, `{"id":"answer","model":"`+body.Model+`","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}]}`)
}
func (v *routingAliasVendor) take() []routingAliasCall {
	v.mu.Lock()
	defer v.mu.Unlock()
	out := slices.Clone(v.calls)
	v.calls = nil
	return out
}
func routingAliasSend(h http.Handler, model, account string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", "http://localhost/v1/chat/completions", strings.NewReader(`{"model":"`+model+`","messages":[{"role":"user","content":"hello"}]}`))
	r.RemoteAddr = "127.0.0.1:12345"
	if account != "" {
		r.Header.Set("X-Magpie-Account", account)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
func routingAliasFixture(t *testing.T) (*routingAliasVendor, http.Handler, func(map[string]any)) {
	t.Helper()
	fresh(t)
	v := &routingAliasVendor{refused: map[string]int{}}
	up := httptest.NewServer(v)
	t.Cleanup(up.Close)
	p := provider.Provider{ID: "alias-relay", Name: "Alias relay", Chat: up.URL + "/v1", Key: "k-ordinary", Keys: []provider.KeyAccount{{Key: "k-work"}}, Models: []string{"m"}, Fallback: []string{"alias-spare/m"}}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	if err := provider.Save(provider.Provider{ID: "alias-spare", Name: "Spare", Chat: up.URL + "/v1", Key: "k-spare", Models: []string{"m"}}); err != nil {
		t.Fatal(err)
	}
	h := New().Front()
	// Prove that this exact server stack, endpoint, auth and body reach the fake.
	w := routingAliasSend(h, "alias-relay/m", "")
	if w.Code != 200 {
		t.Fatalf("baseline ordinary route: %d %s", w.Code, w.Body.String())
	}
	if got := v.take(); !slices.Equal(got, []routingAliasCall{{"k-ordinary", "m"}}) {
		t.Fatalf("baseline upstream = %v", got)
	}
	update := func(aliases map[string]any) {
		t.Helper()
		b, err := os.ReadFile(provider.Path())
		if err != nil {
			t.Fatal(err)
		}
		var cfg map[string]any
		if err = json.Unmarshal(b, &cfg); err != nil {
			t.Fatal(err)
		}
		for _, raw := range cfg["providers"].([]any) {
			row := raw.(map[string]any)
			if row["id"] == "alias-relay" {
				row["keyAliases"] = aliases
			}
		}
		b, err = json.Marshal(cfg)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(provider.Path(), b, 0600); err != nil {
			t.Fatal(err)
		}
		filememo.Forget()
		catalog.Touched()
	}
	update(map[string]any{"work": map[string]any{"ownerId": provider.KeyID("k-work"), "keyId": provider.KeyID("k-work"), "active": true}})
	return v, h, update
}

func TestRoutingAliasQualifiedUsesOnlyBoundKey(t *testing.T) {
	v, h, _ := routingAliasFixture(t)
	w := routingAliasSend(h, "alias-relay/m@work", "")
	if w.Code != 200 {
		t.Errorf("qualified route status = %d: %s", w.Code, w.Body.String())
	}
	if got := v.take(); !slices.Equal(got, []routingAliasCall{{"k-work", "m"}}) {
		t.Errorf("qualified upstream = %v; want only bound key k-work with plain model m", got)
	}
}
func TestRoutingAliasOrdinaryPoolExcludesAliasedKey(t *testing.T) {
	v, h, _ := routingAliasFixture(t)
	v.mu.Lock()
	v.refused["k-ordinary"] = 429
	v.mu.Unlock()
	w := routingAliasSend(h, "alias-relay/m", "")
	got := v.take()
	for _, c := range got {
		if c.Key == "k-work" {
			t.Errorf("ordinary route consumed aliased key: %v", got)
		}
	}
	if w.Code != 200 || !slices.Contains(got, routingAliasCall{"k-spare", "m"}) {
		t.Errorf("ordinary fallback = %d %s, calls %v; want configured spare", w.Code, w.Body.String(), got)
	}
}
func TestRoutingAliasQualifiedFailureNeverFallsBack(t *testing.T) {
	for _, status := range []int{429, 500} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			v, h, _ := routingAliasFixture(t)
			v.mu.Lock()
			v.refused["k-work"] = status
			v.mu.Unlock()
			w := routingAliasSend(h, "alias-relay/m@work", "")
			if w.Code < 400 {
				t.Errorf("bound target failure returned success: %d %s", w.Code, w.Body.String())
			}
			got := v.take()
			if len(got) == 0 {
				t.Error("known active target was never called")
			}
			for _, c := range got {
				if c.Key != "k-work" || c.Model != "m" {
					t.Errorf("qualified failure escaped bound target: %v", got)
				}
			}
		})
	}
}
func TestRoutingAliasUnknownAndRetiredFailLocally(t *testing.T) {
	for _, alias := range []string{"unknown", "retired"} {
		t.Run(alias, func(t *testing.T) {
			v, h, update := routingAliasFixture(t)
			if alias == "retired" {
				update(map[string]any{"retired": map[string]any{"ownerId": provider.KeyID("k-work"), "keyId": provider.KeyID("k-work"), "active": false}})
			}
			w := routingAliasSend(h, "alias-relay/m@"+alias, "")
			if w.Code != 404 {
				t.Errorf("%s alias status = %d: %s; want local 404", alias, w.Code, w.Body.String())
			}
			if !strings.Contains(strings.ToLower(w.Body.String()), "alias") {
				t.Errorf("missing alias reason: %s", w.Body.String())
			}
			if got := v.take(); len(got) != 0 {
				t.Errorf("invalid alias consumed upstream: %v", got)
			}
		})
	}
}
func TestRoutingAliasCatalogKeepsBaseAndAddsQualified(t *testing.T) {
	_, _, _ = routingAliasFixture(t)
	base, qualified := false, false
	for _, e := range provider.Catalog() {
		if e.ID == "alias-relay/m" {
			base = true
		}
		if e.ID == "alias-relay/m@work" {
			qualified = true
			if e.Model != "m" {
				t.Errorf("qualified catalog plain model = %q", e.Model)
			}
		}
	}
	if !base || !qualified {
		t.Errorf("catalog base=%v qualified=%v; both must be discoverable", base, qualified)
	}
}
func TestRoutingAliasNativeAtRemainsPlain(t *testing.T) {
	for _, model := range []string{"m@500k", "m@20260801", "@cf/vendor/m", "m@work/submodel"} {
		t.Run(model, func(t *testing.T) {
			v, h, _ := routingAliasFixture(t)
			w := routingAliasSend(h, "alias-relay/"+model, "")
			if w.Code != 200 {
				t.Errorf("native model: %d %s", w.Code, w.Body.String())
			}
			if got := v.take(); !slices.Equal(got, []routingAliasCall{{"k-ordinary", model}}) {
				t.Errorf("native upstream = %v", got)
			}
		})
	}
}
