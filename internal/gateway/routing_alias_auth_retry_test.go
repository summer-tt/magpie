package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

type aliasAuthAttempt struct{ X, Authorization, Model, Path string }

// The first refusal is held only after the initial request really arrived.
// No provider lock or request cancellation supplies the dispatch barrier.
type aliasAuthBarrier struct {
	mu      sync.Mutex
	calls   []aliasAuthAttempt
	armed   bool
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (b *aliasAuthBarrier) arrive(r *http.Request) error {
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		return err
	}
	r.Body = io.NopCloser(bytes.NewReader(raw))
	var body struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return err
	}
	b.mu.Lock()
	b.calls = append(b.calls, aliasAuthAttempt{r.Header.Get("x-api-key"), r.Header.Get("Authorization"), body.Model, r.URL.Path})
	block := b.armed
	b.armed = false
	b.mu.Unlock()
	if block {
		close(b.entered)
		select {
		case <-b.release:
		case <-r.Context().Done():
			return r.Context().Err()
		}
	}
	return nil
}

func (b *aliasAuthBarrier) take() []aliasAuthAttempt {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := append([]aliasAuthAttempt(nil), b.calls...)
	b.calls = nil
	return out
}
func (b *aliasAuthBarrier) arm()     { b.mu.Lock(); b.armed = true; b.mu.Unlock() }
func (b *aliasAuthBarrier) unblock() { b.once.Do(func() { close(b.release) }) }

// glmKeys supplies the real 1113 and Coding Plan response fixture. Every
// hostname, including the real Zhipu spelling, terminates at this transport.
type aliasGLMRetryTransport struct {
	barrier *aliasAuthBarrier
	vendor  *glmKeys
}

func (f aliasGLMRetryTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if err := f.barrier.arrive(r); err != nil {
		return nil, err
	}
	return f.vendor.RoundTrip(r)
}

func aliasRetryRequest(ctx context.Context, h http.Handler, path, ref string) *httptest.ResponseRecorder {
	body := `{"model":"` + ref + `","max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`
	r := httptest.NewRequest(http.MethodPost, "http://localhost"+path, strings.NewReader(body)).WithContext(ctx)
	r.RemoteAddr = "127.0.0.1:1234"
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func aliasRetryChangeBinding(t *testing.T, id, key, mode string) {
	t.Helper()
	name := "isolated"
	if mode == "qualified-retired" {
		name = ""
	}
	if err := provider.SetKeyRoutingAlias(id, provider.KeyID(key), name); err != nil {
		t.Fatal(err)
	}
}

func aliasRetryWhileChangingBinding(t *testing.T, b *aliasAuthBarrier, send func(context.Context) *httptest.ResponseRecorder, change func()) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	var wg sync.WaitGroup
	t.Cleanup(func() { cancel(); b.unblock(); wg.Wait() })
	done := make(chan *httptest.ResponseRecorder, 1)
	b.arm()
	wg.Add(1)
	go func() { defer wg.Done(); done <- send(ctx) }()
	select {
	case <-b.entered:
	case w := <-done:
		t.Fatalf("fixture ended before first upstream refusal: %d %s", w.Code, w.Body.String())
	case <-ctx.Done():
		t.Fatal("first upstream request did not reach barrier")
	}
	change()
	b.unblock()
	var w *httptest.ResponseRecorder
	select {
	case w = <-done:
	case <-ctx.Done():
		t.Fatal("retry did not finish after barrier release")
	}
	if ctx.Err() != nil {
		t.Fatalf("canceled request is not a binding result: %v", ctx.Err())
	}
	calls := b.take()
	if len(calls) != 1 {
		t.Errorf("retry sent after binding changed: %+v; want the initial request only", calls)
	}
	if w.Code < 400 {
		t.Errorf("request succeeded after binding changed: %d %s", w.Code, w.Body.String())
	}
}

func TestRoutingAliasAuthRetryRechecksCurrentBinding(t *testing.T) {
	for _, only := range []string{provider.AuthXAPIKey, provider.AuthBearer} {
		for _, mode := range []string{"ordinary-isolated", "qualified-retired"} {
			t.Run(only+"/"+mode, func(t *testing.T) {
				fresh(t)
				b := &aliasAuthBarrier{entered: make(chan struct{}), release: make(chan struct{})}
				vendor := &adaptiveVendor{}
				up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if err := b.arrive(r); err != nil {
						return
					}
					x, a := r.Header.Get("x-api-key"), r.Header.Get("Authorization")
					if x != "" && a != "" || only == provider.AuthBearer && x != "" {
						w.Header().Set("Content-Type", "application/json")
						w.WriteHeader(http.StatusUnauthorized)
						io.WriteString(w, `{"error":{"type":"authentication_error","message":"鉴权 header， x-api-key 和 Authorization 不可以同时存在"}}`)
						return
					}
					vendor.ServeHTTP(w, r)
				}))
				t.Cleanup(up.Close)
				t.Cleanup(b.unblock)
				const id, key, model = "alias-auth-retry", "fixture-auth-key", "claude-sonnet-4-5"
				if err := provider.Save(provider.Provider{ID: id, Name: id, Key: key, Anthropic: up.URL, Models: []string{model}, Affinity: "off"}); err != nil {
					t.Fatal(err)
				}
				s := New()
				h := s.Front()
				ref := id + "/" + model
				reset := func() {
					t.Helper()
					p, err := provider.Find(id)
					if err != nil {
						t.Fatal(err)
					}
					p.AuthHeader = provider.AuthBoth
					if err := provider.Save(*p); err != nil {
						t.Fatal(err)
					}
					p, err = provider.Find(id)
					if err != nil || p.AuthHeader != provider.AuthBoth || !p.BothAuth() {
						t.Fatalf("AuthBoth was not restored: %v %+v", err, p)
					}
				}
				prove := func() {
					t.Helper()
					w := aliasRetryRequest(t.Context(), h, "/v1/messages", ref)
					if w.Code != 200 {
						t.Fatalf("legal Messages retry baseline: %d %s", w.Code, w.Body.String())
					}
					got := b.take()
					want := 2
					if only == provider.AuthBearer {
						want = 3
					}
					if len(got) != want || got[0].X != key || got[0].Authorization != "Bearer "+key {
						t.Fatalf("baseline did not execute auth retry: %+v", got)
					}
					for _, call := range got {
						if call.Model != model {
							t.Fatalf("upstream model was not base: %+v", got)
						}
					}
					last := got[len(got)-1]
					if only == provider.AuthXAPIKey && (last.X != key || last.Authorization != "") || only == provider.AuthBearer && (last.X != "" || last.Authorization != "Bearer "+key) {
						t.Fatalf("wrong successful auth alternative: %+v", got)
					}
					p, err := provider.Find(id)
					if err != nil || p.AuthHeader != only {
						t.Fatalf("successful retry did not LearnAuthHeader: %v %+v", err, p)
					}
					reset() // Learning must not make the negative case silently skip retry.
				}
				prove()
				if mode == "qualified-retired" {
					if err := provider.SetKeyRoutingAlias(id, provider.KeyID(key), "work"); err != nil {
						t.Fatal(err)
					}
					ref += "@work"
					prove()
				}
				aliasRetryWhileChangingBinding(t, b, func(ctx context.Context) *httptest.ResponseRecorder {
					return aliasRetryRequest(ctx, h, "/v1/messages", ref)
				}, func() { aliasRetryChangeBinding(t, id, key, mode) })
			})
		}
	}
}

func TestRoutingAliasGLMRetryRechecksCurrentBinding(t *testing.T) {
	for _, mode := range []string{"ordinary-isolated", "qualified-retired"} {
		t.Run(mode, func(t *testing.T) {
			fresh(t)
			b := &aliasAuthBarrier{entered: make(chan struct{}), release: make(chan struct{})}
			t.Cleanup(b.unblock)
			const id, key, model = "alias-glm-retry", "plan-key", "glm-5.1"
			p, err := provider.FromPreset("zhipu")
			if err != nil {
				t.Fatal(err)
			}
			p.ID, p.Name, p.Key, p.Models = id, id, key, []string{model}
			payg := p.Chat
			if err := provider.Save(p); err != nil {
				t.Fatal(err)
			}
			fake := &glmKeys{payg: map[string]bool{}, plan: map[string]bool{key: true}}
			s := New()
			s.client = &http.Client{Transport: aliasGLMRetryTransport{b, fake}}
			h := s.Front()
			ref := id + "/" + model
			prove := func() {
				t.Helper()
				w := aliasRetryRequest(t.Context(), h, "/v1/chat/completions", ref)
				if w.Code != 200 {
					t.Fatalf("legal GLM retry baseline: %d %s", w.Code, w.Body.String())
				}
				got := b.take()
				if len(got) != 2 || got[0].Path != "/api/paas/v4/chat/completions" || got[1].Path != "/api/coding/paas/v4/chat/completions" || got[1].Model != model {
					t.Fatalf("baseline did not reach 1113 Coding retry: %+v", got)
				}
				q, err := provider.Find(id)
				if err != nil || !strings.Contains(q.Chat, "/api/coding/paas/v4") {
					t.Fatalf("Coding Plan success was not learned: %v %+v", err, q)
				}
				// As for AuthHeader, restore the original endpoint after learning it.
				q.Chat, q.Responses = payg, ""
				if err := provider.Save(*q); err != nil {
					t.Fatal(err)
				}
			}
			prove()
			if mode == "qualified-retired" {
				if err := provider.SetKeyRoutingAlias(id, provider.KeyID(key), "work"); err != nil {
					t.Fatal(err)
				}
				ref += "@work"
				prove()
			}
			aliasRetryWhileChangingBinding(t, b, func(ctx context.Context) *httptest.ResponseRecorder {
				return aliasRetryRequest(ctx, h, "/v1/chat/completions", ref)
			}, func() { aliasRetryChangeBinding(t, id, key, mode) })
		})
	}
}
