package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/filememo"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// Use the real Codex auth fixture and backend wire format, including the
// single-account path that can relay without constructing a routing plan.
func TestRoutingAliasCodexSingleAccountBareRelay(t *testing.T) {
	codexSignedIn(t)
	var mu sync.Mutex
	var calls []routingAliasCall
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/models") {
			io.WriteString(w, `{"models":[{"slug":"gpt-5.5","display_name":"GPT","visibility":"list"}]}`)
			return
		}
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		calls = append(calls, routingAliasCall{r.Header.Get("chatgpt-account-id"), modelOf(b)})
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(`data: {"type":"response.created","response":{"id":"r1","model":"gpt-5.5"}}`, `data: {"type":"response.output_text.delta","delta":"hello"}`, `data: {"type":"response.completed","response":{"id":"r1","usage":{"input_tokens":7,"output_tokens":1}}}`))
	}))
	t.Cleanup(up.Close)
	old := provider.CodexBase
	provider.CodexBase = up.URL + "/backend-api/codex"
	t.Cleanup(func() { provider.CodexBase = old })
	send := func(model string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "http://localhost"+CodexPath+"/responses", strings.NewReader(`{"model":"`+model+`","input":"hello","stream":true}`))
		r.RemoteAddr = "127.0.0.1:12345"
		r.Header.Set("Authorization", "Bearer chatgpt-token")
		r.Header.Set("chatgpt-account-id", "acct-1")
		w := httptest.NewRecorder()
		New().Front().ServeHTTP(w, r)
		return w
	}
	if w := send("gpt-5.5"); w.Code != 200 {
		t.Fatalf("baseline bare relay: %d %s", w.Code, w.Body.String())
	}
	mu.Lock()
	baseline := slices.Clone(calls)
	calls = nil
	mu.Unlock()
	if !slices.Equal(baseline, []routingAliasCall{{"acct-1", "gpt-5.5"}}) {
		t.Fatalf("baseline bare calls=%v", baseline)
	}
	owner := provider.LoginID("codex", "me@example.com")
	putProvider(t, map[string]any{"id": "codex", "accountAliases": map[string]any{"work": map[string]any{"accountId": owner, "active": true}}})
	filememo.Forget()
	catalog.Touched()
	if w := send("gpt-5.5"); w.Code < 400 {
		t.Errorf("ordinary bare relay used isolated only account: status=%d", w.Code)
	}
	mu.Lock()
	ordinary := slices.Clone(calls)
	calls = nil
	mu.Unlock()
	if len(ordinary) != 0 {
		t.Errorf("ordinary bare relay consumed isolated account: %v", ordinary)
	}
	if w := send("codex/gpt-5.5@work"); w.Code != 200 {
		t.Errorf("qualified Codex relay: %d %s", w.Code, w.Body.String())
	}
	mu.Lock()
	qualified := slices.Clone(calls)
	mu.Unlock()
	if !slices.Equal(qualified, []routingAliasCall{{"acct-1", "gpt-5.5"}}) {
		t.Errorf("qualified Codex calls=%v; want original auth and plain model", qualified)
	}
}

func TestRoutingAliasCodexSearchNeverEscapesOwner(t *testing.T) {
	for _, mode := range []string{"ordinary", "qualified", "unknown", "qualified-failure"} {
		t.Run(mode, func(t *testing.T) {
			codexSignedIn(t, "spare@example.com")
			st := settings.Load()
			st.Searcher = "off"
			if err := settings.Save(st); err != nil {
				t.Fatal(err)
			}
			var mu sync.Mutex
			var calls []routingAliasCall
			fail := false
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/models") {
					io.WriteString(w, `{"models":[{"slug":"gpt-5.5","visibility":"list"}]}`)
					return
				}
				b, _ := io.ReadAll(r.Body)
				owner := r.Header.Get("chatgpt-account-id")
				mu.Lock()
				calls = append(calls, routingAliasCall{owner, modelOf(b)})
				refuse := fail && owner == "acct-1"
				mu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				if refuse {
					w.WriteHeader(429)
					io.WriteString(w, `{"error":{"type":"usage_limit_reached","message":"target quota"}}`)
					return
				}
				io.WriteString(w, `{"encrypted_output":"fixture","output":"search result","results":[]}`)
			}))
			t.Cleanup(up.Close)
			old := provider.CodexBase
			provider.CodexBase = up.URL + "/backend-api/codex"
			t.Cleanup(func() { provider.CodexBase = old })
			h := New().Front()
			send := func(model string) *httptest.ResponseRecorder {
				r := httptest.NewRequest("POST", "http://localhost/v1/alpha/search", strings.NewReader(`{"model":"`+model+`","commands":{"search_query":[{"q":"fixture query"}]}}`))
				r.RemoteAddr = "127.0.0.1:12345"
				w := httptest.NewRecorder()
				h.ServeHTTP(w, r)
				return w
			}
			if w := send("codex/gpt-5.5"); w.Code != 200 || !strings.Contains(w.Body.String(), "search result") {
				t.Fatalf("baseline search: %d %s", w.Code, w.Body.String())
			}
			mu.Lock()
			baseline := slices.Clone(calls)
			calls = nil
			mu.Unlock()
			if !slices.Equal(baseline, []routingAliasCall{{"acct-1", "gpt-5.5"}}) {
				t.Fatalf("baseline search calls=%v", baseline)
			}
			owner := provider.LoginID("codex", "me@example.com")
			putProvider(t, map[string]any{"id": "codex", "accountAliases": map[string]any{"work": map[string]any{"accountId": owner, "active": true}}})
			filememo.Forget()
			catalog.Touched()
			model := "codex/gpt-5.5"
			if mode != "ordinary" {
				model += "@work"
			}
			if mode == "unknown" {
				model = "codex/gpt-5.5@unknown"
			}
			if mode == "qualified-failure" {
				mu.Lock()
				fail = true
				mu.Unlock()
			}
			w := send(model)
			mu.Lock()
			got := slices.Clone(calls)
			mu.Unlock()
			switch mode {
			case "ordinary":
				if w.Code != 200 || !slices.Equal(got, []routingAliasCall{{"acct-2", "gpt-5.5"}}) {
					t.Errorf("ordinary search: status=%d calls=%v; only unaliased spare may answer", w.Code, got)
				}
			case "qualified":
				if w.Code != 200 || !slices.Equal(got, []routingAliasCall{{"acct-1", "gpt-5.5"}}) {
					t.Errorf("qualified search: status=%d calls=%v", w.Code, got)
				}
			case "unknown":
				if w.Code != 404 || len(got) != 0 {
					t.Errorf("unknown search alias: status=%d calls=%v; want local404", w.Code, got)
				}
			case "qualified-failure":
				if len(got) == 0 {
					t.Error("active qualified search never reached target")
				}
				for _, c := range got {
					if c.Key != "acct-1" || c.Model != "gpt-5.5" {
						t.Errorf("failed qualified search escaped owner: %v", got)
					}
				}
				if w.Code < 400 {
					t.Errorf("failed qualified search returned success=%d instead of target failure", w.Code)
				}
			}
		})
	}
}

func TestRoutingAliasCountRejectsInvalidReference(t *testing.T) {
	for _, protocol := range []string{"anthropic", "gemini"} {
		t.Run(protocol, func(t *testing.T) {
			v, h, update := routingAliasFixture(t)
			count := func(ref string) *httptest.ResponseRecorder {
				path, body := "/v1/messages/count_tokens", `{"model":"`+ref+`","messages":[{"role":"user","content":"hello"}]}`
				if protocol == "gemini" {
					path = "/v1beta/models/" + ref + ":countTokens"
					body = `{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`
				}
				r := httptest.NewRequest("POST", "http://localhost"+path, strings.NewReader(body))
				r.RemoteAddr = "127.0.0.1:12345"
				w := httptest.NewRecorder()
				h.ServeHTTP(w, r)
				return w
			}
			if w := count("alias-relay/m"); w.Code != 200 {
				t.Fatalf("baseline count %s: %d %s", protocol, w.Code, w.Body.String())
			}
			v.take()
			if w := count("alias-relay/m@work"); w.Code != 200 {
				t.Errorf("active qualified count: %d %s", w.Code, w.Body.String())
			}
			for _, c := range v.take() {
				if c.Key != "k-work" || c.Model != "m" {
					t.Errorf("qualified count escaped target: %v", c)
				}
			}
			update(map[string]any{"old": routingAliasBinding("k-work", false)})
			for _, alias := range []string{"unknown", "old"} {
				w := count("alias-relay/m@" + alias)
				if w.Code != 404 {
					t.Errorf("%s count invalid alias %s status=%d; want404", protocol, alias, w.Code)
				}
				if got := v.take(); len(got) != 0 {
					t.Errorf("invalid count consumed upstream=%v", got)
				}
			}
		})
	}
}
