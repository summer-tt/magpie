package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

// Embeddings' retrieveAs and decisions' postDecide both create an unpaid
// ticket in metered. Unlike the main chat try, neither waits before its
// RoutingCurrent check. The real rpmTransport therefore owns this wait.
func TestRoutingAliasRPMDispatchRechecksCurrentBinding(t *testing.T) {
	for _, operation := range []string{"embedding", "decision"} {
		for _, mode := range []string{"ordinary-isolated", "qualified-retired"} {
			t.Run(operation+"/"+mode, func(t *testing.T) {
				fresh(t)
				clock := holdMinute(t)
				waiting := make(chan time.Duration, 8)
				previousTimer := rpmTimer
				rpmTimer = func(d time.Duration) (<-chan time.Time, func() bool) {
					c, stop := previousTimer(d)
					select {
					case waiting <- d:
					default:
					}
					return c, stop
				}
				t.Cleanup(func() { rpmTimer = previousTimer })
				var mu sync.Mutex
				var calls []routingAliasCall
				const id, key = "alias-rpm-send", "fixture-rpm-key"
				model := "embed-1"
				if operation == "decision" {
					model = "typesafe/jev"
				}
				up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					var body struct {
						Model string `json:"model"`
					}
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						http.Error(w, "invalid fixture JSON", 400)
						return
					}
					mu.Lock()
					calls = append(calls, routingAliasCall{strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), body.Model})
					mu.Unlock()
					w.Header().Set("Content-Type", "application/json")
					switch r.URL.Path {
					case "/v1/embeddings":
						io.WriteString(w, `{"object":"list","model":"embed-1","data":[{"object":"embedding","index":0,"embedding":[0.1]}],"usage":{"prompt_tokens":2,"total_tokens":2}}`)
					case "/v1/systemone":
						io.WriteString(w, `{"model":"typesafe/jev","answers":{"intent":{"type":"choice","choice":"bug","confidence":0.9}}}`)
					default:
						http.NotFound(w, r)
					}
				}))
				t.Cleanup(up.Close)
				p := provider.Provider{ID: id, Name: id, Chat: up.URL + "/v1", Decide: up.URL + "/v1", Key: key, Models: []string{model}, MaxRPM: 1, Affinity: "off"}
				if err := provider.Save(p); err != nil {
					t.Fatal(err)
				}
				if operation == "decision" {
					// A mixed Chat/Decide provider's decision models come from
					// its live list, as in TestSystemOneRoutesByPrefix's listed Jev.
					if err := catalog.SaveLive(id, p.Chat, []catalog.Model{{ID: model, Name: "Jev"}}); err != nil {
						t.Fatal(err)
					}
				}
				s := New()
				h := s.Front()
				ref := id + "/" + model
				send := func(ctx context.Context) *httptest.ResponseRecorder {
					path, body := "/v1/embeddings", `{"model":"`+ref+`","input":["hello"]}`
					if operation == "decision" {
						path, body = "/v1/systemone", `{"model":"`+ref+`","state":{"message":"hello"},"questions":{"intent":{"type":"choice"}}}`
					}
					r := httptest.NewRequest(http.MethodPost, "http://localhost"+path, strings.NewReader(body)).WithContext(ctx)
					r.RemoteAddr = "127.0.0.1:1234"
					r.Header.Set("Content-Type", "application/json")
					w := httptest.NewRecorder()
					h.ServeHTTP(w, r)
					return w
				}
				take := func() []routingAliasCall {
					mu.Lock()
					defer mu.Unlock()
					out := append([]routingAliasCall(nil), calls...)
					calls = nil
					return out
				}
				checkSuccess := func(w *httptest.ResponseRecorder) {
					t.Helper()
					if w.Code != 200 {
						t.Fatalf("legal %s baseline: %d %s", operation, w.Code, w.Body.String())
					}
					got := take()
					if len(got) != 1 || got[0].Key != key || got[0].Model != model {
						t.Fatalf("baseline owner/base model: %+v", got)
					}
				}
				checkSuccess(send(t.Context())) // A real successful send consumes the first ticket.
				if mode == "qualified-retired" {
					if err := provider.SetKeyRoutingAlias(id, provider.KeyID(key), "work"); err != nil {
						t.Fatal(err)
					}
					ref += "@work"
				}
				ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
				var wg sync.WaitGroup
				t.Cleanup(func() { cancel(); clock.release(); wg.Wait() })
				queue := func() <-chan *httptest.ResponseRecorder {
					t.Helper()
					done := make(chan *httptest.ResponseRecorder, 1)
					wg.Add(1)
					go func() { defer wg.Done(); done <- send(ctx) }()
					select {
					case d := <-waiting:
						if d <= 0 {
							t.Fatalf("transport reported nonpositive wait: %s", d)
						}
					case w := <-done:
						t.Fatalf("request ended before transport RPM wait: %d %s", w.Code, w.Body.String())
					case <-ctx.Done():
						t.Fatal("request did not enter transport RPM wait")
					}
					if got := take(); len(got) != 0 {
						t.Fatalf("request was already sent before RPM release: %+v", got)
					}
					return done
				}
				finish := func(done <-chan *httptest.ResponseRecorder) *httptest.ResponseRecorder {
					t.Helper()
					select {
					case w := <-done:
						if ctx.Err() != nil {
							t.Fatalf("cancellation is not a routing result: %v", ctx.Err())
						}
						return w
					case <-ctx.Done():
						t.Fatal("request did not finish after RPM capacity was released")
					}
					return nil
				}
				// First prove a queued request on the unchanged binding succeeds. This
				// also proves qualified resolution and the held timer are legal fixtures.
				done := queue()
				clock.advance(rpmSpan())
				checkSuccess(finish(done))
				t.Log("legal queued baseline succeeded after held RPM capacity was released")

				done = queue()
				aliasRetryChangeBinding(t, id, key, mode)
				clock.advance(rpmSpan()) // Release capacity; the request stays live.
				w := finish(done)
				got := take()
				if w.Code < 400 {
					t.Errorf("%s RPM waiter succeeded after %s: %d %s", operation, mode, w.Code, w.Body.String())
				}
				if len(got) != 0 {
					t.Errorf("%s RPM waiter consumed a key after %s: %+v", operation, mode, got)
				}

			})
		}
	}
}
