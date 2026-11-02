package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// A native lead may move an ordinary subagent onto its model, but a
// qualified request must keep the provider and key its alias names.
func TestRoutingAliasCodexSubagentLeadKeepsQualifiedOwner(t *testing.T) {
	chatgpt := codexSubagentSetup(t)
	const clientKey = "sk-lead-client-fixture"
	const keyB = "fixture-provider-key-B"
	var mu sync.Mutex
	var providerCalls, nativeCalls []routingAliasCall
	endpoint := func(calls *[]routingAliasCall) *httptest.Server {
		up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/responses") {
				http.NotFound(w, r)
				return
			}
			b, err := io.ReadAll(r.Body)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			mu.Lock()
			*calls = append(*calls, routingAliasCall{strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), modelOf(b)})
			mu.Unlock()
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, sse(
				`data: {"type":"response.created","response":{"id":"r1","model":"`+modelOf(b)+`"}}`,
				`data: {"type":"response.output_text.delta","delta":"on it"}`,
				`data: {"type":"response.completed","response":{"id":"r1","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"on it"}]}],"usage":{"input_tokens":7,"output_tokens":1}}}`))
		}))
		t.Cleanup(up.Close)
		return up
	}
	bound := endpoint(&providerCalls)
	native := endpoint(&nativeCalls)
	oldAPI := codexAPIBase
	codexAPIBase = native.URL + "/v1"
	t.Cleanup(func() { codexAPIBase = oldAPI })
	if err := provider.Save(provider.Provider{ID: "p", Name: "Qualified provider", Responses: bound.URL + "/v1", Key: keyB, Models: []string{"m"}}); err != nil {
		t.Fatal(err)
	}
	if err := provider.SetKeyRoutingAlias("p", provider.KeyID(keyB), "work"); err != nil {
		t.Fatal(err)
	}
	s := New()
	gateway := httptest.NewServer(s.Front())
	t.Cleanup(gateway.Close)
	// Keep the package's global upstream transport; both native bases and
	// the provider endpoint point to local fake servers.
	send := func(model, session, kind, input string) {
		t.Helper()
		body := `{"model":"` + model + `","stream":true,"input":[` + input + `]}`
		r, err := http.NewRequestWithContext(t.Context(), http.MethodPost, gateway.URL+CodexPath+"/responses", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		r.Header.Set("Authorization", "Bearer "+clientKey)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("User-Agent", "codex_cli_rs/0.162.1 (Linux 6.8; x86_64)")
		r.Header.Set("session_id", session)
		if kind != "" {
			r.Header.Set("x-openai-subagent", kind)
			r.Header.Set("x-codex-parent-thread-id", "lead-1")
		}
		resp, err := gateway.Client().Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, err := io.ReadAll(resp.Body)
		if err != nil || resp.StatusCode != http.StatusOK || !strings.Contains(string(b), "on it") || !strings.Contains(string(b), "response.completed") {
			t.Fatalf("%s thread %s: status=%d body=%s read=%v; want a successful completed reply", model, session, resp.StatusCode, b, err)
		}
	}
	take := func() ([]routingAliasCall, []routingAliasCall) {
		mu.Lock()
		defer mu.Unlock()
		p, n := slices.Clone(providerCalls), slices.Clone(nativeCalls)
		providerCalls, nativeCalls = nil, nil
		return p, n
	}
	// First prove this alias succeeds on key B through the backend stack.
	send("p/m@work", "qualified-baseline", "collab_spawn", plainTask)
	if p, n := take(); !slices.Equal(p, []routingAliasCall{{keyB, "m"}}) || len(n) != 0 {
		t.Fatalf("qualified baseline: provider=%v native=%v; want only key B on m", p, n)
	}
	t.Log("qualified baseline PASS: provider p received key B and base model m; native received zero requests")
	if err := provider.SetCodexSubagentModel(provider.CodexSubagentLead); err != nil {
		t.Fatal(err)
	}
	send("gpt-5.5", "lead-1", "", leadAsk)
	if p, n := take(); len(p) != 0 || !slices.Equal(n, []routingAliasCall{{clientKey, "gpt-5.5"}}) {
		t.Fatalf("native lead: provider=%v native=%v; want the client's API key on gpt-5.5", p, n)
	}
	t.Log("native lead PASS: bare gpt-5.5 completed with the client API key")
	// A different worker thread prevents resume affinity from hiding the
	// ordinary lead rewrite that must remain supported.
	send("p/m", "ordinary-worker", "collab_spawn", plainTask)
	if p, n := take(); len(p) != 0 || !slices.Equal(n, []routingAliasCall{{clientKey, "gpt-5.5"}}) {
		t.Fatalf("ordinary subagent: provider=%v native=%v; want the native lead model and client API key", p, n)
	}
	t.Log("ordinary subagent PASS: followed lead to native gpt-5.5 with the client API key")
	send("p/m@work", "qualified-worker", "collab_spawn", plainTask)
	p, n := take()
	if !slices.Equal(p, []routingAliasCall{{keyB, "m"}}) {
		t.Errorf("qualified subagent lost its owner: provider=%v; want only provider p key B on base model m", p)
	}
	if len(n) != 0 {
		t.Errorf("qualified subagent crossed provider with the client's API key: native=%v; want zero additional native requests", n)
	}
	if got := chatgpt.asked(); len(got) != 0 {
		t.Errorf("API-key requests reached the ChatGPT subscription backend: %v", got)
	}
}
