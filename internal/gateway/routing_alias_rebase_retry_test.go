package gateway

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// The rebase meet this file holds: upstream's RetryOpenCodeFreeOnZen asks
// Zen's free tier again with the public key, signed out, while the alias
// feature binds a qualified reference to one account whose credentials
// alone may serve it. An ordinary OpenCode Go request keeps upstream's
// behavior (Go's "Model is unavailable" retried at Zen, answered there); a
// qualified one must not be re-asked as anonymous — that would serve the
// request off the bound account with a credential nobody chose, so the
// refusal stands and Zen is never asked.

type zenRetryAsk struct{ url, auth, apiKey, model string }

// zenRetryTransport terminates every opencode.ai request at a fake: Go's
// gateway answers the same "Model is unavailable" refusal the real one
// gave on 2026-11-02 (goUnavailable, opencode_go_free_test.go), and Zen's
// free tier answers a stream saying "from zen". No request leaves the test.
type zenRetryTransport struct {
	mu    sync.Mutex
	calls []zenRetryAsk
}

func (f *zenRetryTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	b, _ := io.ReadAll(r.Body)
	r.Body.Close()
	r.Body = io.NopCloser(bytes.NewReader(b))
	var body struct {
		Model string `json:"model"`
	}
	_ = json.Unmarshal(b, &body)
	f.mu.Lock()
	f.calls = append(f.calls, zenRetryAsk{r.URL.Host + r.URL.Path, r.Header.Get("Authorization"), r.Header.Get("x-api-key"), body.Model})
	f.mu.Unlock()
	if strings.HasPrefix(r.URL.Path, "/inference/go/") {
		return &http.Response{StatusCode: 400, Header: http.Header{"Content-Type": {"application/json"}},
			Body: io.NopCloser(strings.NewReader(goUnavailable))}, nil
	}
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}},
		Body: io.NopCloser(strings.NewReader(streamFor(r.URL.Path, "from zen")))}, nil
}

func (f *zenRetryTransport) take() []zenRetryAsk {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := append([]zenRetryAsk(nil), f.calls...)
	f.calls = nil
	return out
}

// zenRetryServer is the OpenCode Go provider as the preset adds it
// (goFreeServer's fixture), with the free Zen model listed and Go serving
// none of its own, so Go answers "Model is unavailable" to it.
func zenRetryServer(t *testing.T) (*Server, *zenRetryTransport) {
	t.Helper()
	fresh(t)
	if err := provider.Save(provider.Provider{ID: "go", Name: "OpenCode Go", Preset: "opencode-go", Key: "sk-go-users-own",
		Chat: provider.OpenCodeGoChat, Responses: provider.OpenCodeGoChat, Anthropic: provider.OpenCodeGoAnthropic,
		Models: []string{"deepseek-v4.1-flash-free"}}); err != nil {
		t.Fatal(err)
	}
	tr := &zenRetryTransport{}
	s := New()
	s.client = &http.Client{Transport: tr}
	return s, tr
}

// Upstream's behavior, kept: an ordinary OpenCode Go request for one of
// Zen's free models is asked again at Zen's free tier with the public key,
// never the user's Go key, and Zen's answer is served.
func TestRoutingAliasOrdinaryOpenCodeZenRetryStillServes(t *testing.T) {
	s, asked := zenRetryServer(t)
	rec := goClaudeAsk(s, "go/deepseek-v4.1-flash-free")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "from zen") {
		t.Fatalf("ordinary Zen retry: %d %s", rec.Code, rec.Body)
	}
	a := asked.take()
	// The model's dispatch lands on whichever of Go's APIs the provider
	// legitimately speaks for it (Messages or Chat); the retry is the same
	// request at Zen's free tier beside it, as upstream's fixture maps it.
	want := map[string]string{"opencode.ai/inference/go/anthropic/v1/messages": "opencode.ai/inference/anthropic/v1/messages",
		"opencode.ai/inference/go/openai/v1/chat/completions": "opencode.ai/inference/openai/v1/chat/completions"}
	if len(a) != 2 || want[a[0].url] == "" || a[1].url != want[a[0].url] {
		t.Fatalf("ordinary retry attempts: %+v; want Go then Zen's free tier", a)
	}
	for _, call := range a {
		if call.model != "deepseek-v4.1-flash-free" {
			t.Fatalf("upstream model was not the plain base: %+v", a)
		}
	}
	if z := a[1]; z.auth != "Bearer public" || z.apiKey != "" && z.apiKey != "public" || strings.Contains(z.auth+z.apiKey, "sk-go") {
		t.Fatalf("Zen asked with %+v; want the public key, never the user's", z)
	}
}

// A qualified reference is bound to its account's key alone. Go's refusal
// of the free model reaches the fake with that key — the dispatch itself
// is legal — but the request is never re-asked at Zen as anonymous: the
// refusal comes back an error and Zen's tier is not called at all.
func TestRoutingAliasQualifiedOpenCodeGoRefusalDoesNotFallBackToZen(t *testing.T) {
	s, asked := zenRetryServer(t)
	if err := provider.SetKeyRoutingAlias("go", provider.KeyID("sk-go-users-own"), "work"); err != nil {
		t.Fatal(err)
	}
	rec := goClaudeAsk(s, "go/deepseek-v4.1-flash-free@work")
	a := asked.take()
	if len(a) != 1 || !strings.HasPrefix(a[0].url, "opencode.ai/inference/go/") {
		t.Fatalf("qualified attempts after Go's refusal: %+v; want the initial Go request only", a)
	}
	if !strings.Contains(a[0].auth+a[0].apiKey, "sk-go-users-own") || strings.Contains(a[0].auth+a[0].apiKey, "public") {
		t.Fatalf("initial qualified dispatch did not carry the bound account's key: %+v", a[0])
	}
	if a[0].model != "deepseek-v4.1-flash-free" {
		t.Fatalf("qualified upstream model was not the plain base: %+v", a[0])
	}
	if rec.Code < 400 {
		t.Errorf("qualified request was served after Go's refusal: %d %s; the anonymous Zen fallback is not the bound account", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), "from zen") {
		t.Errorf("qualified request was answered by Zen's free tier: %s", rec.Body)
	}
}
