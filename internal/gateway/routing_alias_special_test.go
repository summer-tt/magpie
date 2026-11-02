package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/middleware"
	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

func TestRoutingAliasBackendSideRequestsKeepOriginalConstraint(t *testing.T) {
	for _, kind := range []string{"thread_title", "thread_description", "composer_predictions"} {
		t.Run(kind, func(t *testing.T) {
			v, h, _ := routingAliasFixture(t)
			send := func(ref, source string) *httptest.ResponseRecorder {
				r := httptest.NewRequest("POST", "http://localhost"+CodexPath+"/responses", strings.NewReader(`{"model":"`+ref+`","input":"hello","stream":true}`))
				r.RemoteAddr = "127.0.0.1:1234"
				r.Header.Set("Authorization", "Bearer chatgpt-token")
				if source != "" {
					r.Header.Set("x-codex-turn-metadata", `{"thread_source":"`+source+`"}`)
				}
				w := httptest.NewRecorder()
				h.ServeHTTP(w, r)
				return w
			}
			if w := send("alias-relay/m@work", ""); w.Code != 200 {
				t.Fatalf("baseline backend qualified=%d %s", w.Code, w.Body.String())
			}
			if got := v.take(); !slices.Equal(got, []routingAliasCall{{"k-work", "m"}}) {
				t.Fatalf("baseline backend owner=%v", got)
			}
			st := settings.Load()
			st.CodexTitles = "alias-spare/m"
			st.CodexDescriptions = "alias-spare/m"
			st.CodexPredictions = "alias-spare/m"
			if err := settings.Save(st); err != nil {
				t.Fatal(err)
			}
			for _, alias := range []string{"work", "unknown"} {
				w := send("alias-relay/m@"+alias, kind)
				got := v.take()
				if alias == "unknown" {
					if w.Code != 404 || len(got) != 0 {
						t.Errorf("backend %s unknown original alias status=%d calls=%v", kind, w.Code, got)
					}
				} else {
					for _, c := range got {
						if c.Key != "k-work" || c.Model != "m" {
							t.Errorf("backend %s escaped original target=%v", kind, got)
						}
					}
					if len(got) == 0 && w.Code < 400 {
						t.Errorf("backend %s silently replaced qualified request", kind)
					}
				}
			}
		})
	}
}
func TestRoutingAliasSynchronousMiddlewareCannotChangeOwner(t *testing.T) {
	v, h, _ := routingAliasFixture(t)
	t.Cleanup(plugin.Settle)
	path := filepath.Join(t.TempDir(), "change-owner.middleware.js")
	if err := os.WriteFile(path, []byte(`export function onRequest(body) { if (body.model.endsWith("@work")) body.model="alias-spare/m"; return body; }`), 0600); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(plugin.List{Plugins: []plugin.Entry{{Spec: path}}})
	if err := os.WriteFile(filepath.Join(settings.Dir(), "plugins.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	middleware.Reload()
	defer middleware.Reload()
	if w := routingAliasSend(h, "alias-relay/m", ""); w.Code != 200 {
		t.Fatalf("baseline content-preserving middleware=%d", w.Code)
	}
	v.take()
	w := routingAliasSend(h, "alias-relay/m@work", "")
	if w.Code < 400 {
		t.Errorf("middleware changed qualified owner and succeeded=%d", w.Code)
	}
	if got := v.take(); len(got) != 0 {
		t.Errorf("middleware owner change consumed upstream=%v", got)
	}
}
func TestRoutingAliasStrictCappedResponderStaysExcluded(t *testing.T) {
	b := strictSetup(t)
	s := New()
	if code, _, tried := strictPost(t, s, b, "strict-alias"); code != 200 || tried != "acct-1" {
		t.Fatalf("baseline strict responder code=%d tried=%s", code, tried)
	}
	if err := provider.SetAccountRoutingAlias("codex", provider.LoginID("codex", "me@example.com"), "work"); err != nil {
		t.Fatal(err)
	}
	capUsage(t, map[string]float64{"me@example.com": 95, "spare@example.com": 10})
	if err := provider.SetAccountCap("codex", "me@example.com", 90); err != nil {
		t.Fatal(err)
	}
	code, _, tried := strictPost(t, s, b, "strict-alias")
	if strings.Contains(tried, "acct-1") {
		t.Errorf("strict cap restoration spent isolated responder: status=%d tried=%s", code, tried)
	}
	// A direct qualified request is still bound at the cap and cannot spend spare.
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"codex/gpt-5.5@work","input":"hello","stream":true}`))
	b.mu.Lock()
	b.tried = nil
	b.mu.Unlock()
	s.Handler().ServeHTTP(w, r)
	tried, _ = b.seen()
	if w.Code != 429 || tried != "" {
		t.Errorf("qualified capped target status=%d tried=%s; want429 no upstream", w.Code, tried)
	}
}
func TestRoutingAliasSealedCompactCannotRecoverIsolatedWriter(t *testing.T) {
	codexSignedIn(t, "biz@example.com")
	token := codexAccessToken(t)
	nativeOn(t, "codex")
	const seal = "gAAAAB-alias-writer"
	wroteCheckpoint(seal, sealedBy("biz@example.com"))
	if code, _, seen := sealedCompactAsked(t, seal, token, ""); code != 200 || seen != "acct-2" {
		t.Fatalf("baseline sealed compact status=%d owner=%s", code, seen)
	}
	if err := provider.SetAccountRoutingAlias("codex", provider.LoginID("codex", "biz@example.com"), "work"); err != nil {
		t.Fatal(err)
	}
	code, _, seen := sealedCompactAsked(t, seal, token, "")
	if code < 400 || seen != "" {
		t.Errorf("ordinary compact recovered isolated seal writer: status=%d owner=%s", code, seen)
	}
}
func TestRoutingAliasPluginFetchUsesOriginalAuthAndPlainModel(t *testing.T) {
	var mu sync.Mutex
	var calls []routingAliasCall
	movedFake(t, "factory", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		calls = append(calls, routingAliasCall{strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), modelOf(b)})
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(`data: {"id":"fixture","choices":[{"index":0,"delta":{"content":"hello"},"finish_reason":"stop"}]}`, `data: [DONE]`))
	}))
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if _, err := plugin.APIKey(ctx, "factory", 0, nil, "k2", plugin.NewAccount); err != nil {
		t.Fatal(err)
	}
	if _, err := plugin.Providers(ctx); err != nil {
		t.Fatal(err)
	}
	h := New().Front()
	if w := routingAliasSend(h, "factory/fake-1", ""); w.Code != 200 {
		t.Fatalf("baseline plugin fetch=%d %s", w.Code, w.Body.String())
	}
	take := func() []routingAliasCall {
		mu.Lock()
		defer mu.Unlock()
		out := slices.Clone(calls)
		calls = nil
		return out
	}
	if got := take(); len(got) == 0 || got[0].Key != "k1" {
		t.Fatalf("baseline plugin auth=%v", got)
	}
	p, err := provider.Find("factory")
	if err != nil {
		t.Fatal(err)
	}
	if err = provider.SetAccountRoutingAlias("factory", p.RoutingID(), "work"); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ ref, key string }{{"factory/fake-1", "k2"}, {"factory/fake-1@work", "k1"}} {
		w := routingAliasSend(h, tc.ref, "")
		got := take()
		if w.Code != 200 || len(got) == 0 {
			t.Errorf("plugin target %s status=%d calls=%v", tc.ref, w.Code, got)
		}
		for _, c := range got {
			if c.Key != tc.key || c.Model != "fake-1" {
				t.Errorf("plugin qualified identity/plain model mismatch=%v want=%s", got, tc.key)
			}
		}
	}
}
