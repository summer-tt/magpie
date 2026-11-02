package gateway

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/filememo"
	"github.com/yetone/magpie/internal/provider"
)

// All state belongs to one test. Only model-list requests are omitted from
// consumption records; every service request records its original fake key.
func aliasMediaSendFixture(t *testing.T, operation string) (string, string, func(context.Context) *httptest.ResponseRecorder, func() []routingAliasCall, func(), <-chan struct{}, func()) {
	t.Helper()
	fresh(t)
	const id = "dispatch-media"
	model := map[string]string{"drawing": "gpt-image-1", "embedding": "embed-1", "decision": "typesafe/jev"}[operation]
	var mu sync.Mutex
	var calls []routingAliasCall
	armed := false
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	var released sync.Once
	unblock := func() { released.Do(func() { close(release) }) }
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/models") {
			io.WriteString(w, `{"data":[{"id":"gpt-image-1","kind":"image"},{"id":"embed-1"},{"id":"typesafe/jev"}]}`)
			return
		}
		var body struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid fixture JSON", 400)
			return
		}
		key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		mu.Lock()
		calls = append(calls, routingAliasCall{key, body.Model})
		blocked := armed && key == "fixture-k1"
		mu.Unlock()
		if blocked {
			select {
			case entered <- struct{}{}:
			default:
			}
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
			w.WriteHeader(429)
			io.WriteString(w, `{"error":{"message":"target rate limit"}}`)
			return
		}
		switch r.URL.Path {
		case "/v1/images/generations":
			io.WriteString(w, `{"created":1,"data":[{"b64_json":"`+base64.StdEncoding.EncodeToString(pngBytes)+`"}]}`)
		case "/v1/embeddings":
			io.WriteString(w, `{"object":"list","model":"embed-1","data":[{"object":"embedding","index":0,"embedding":[0.1]}],"usage":{"prompt_tokens":2,"total_tokens":2}}`)
		case "/v1/systemone":
			io.WriteString(w, `{"model":"typesafe/jev","answers":{"intent":{"type":"choice","choice":"bug","confidence":0.9}}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(up.Close)
	t.Cleanup(unblock)
	p := provider.Provider{ID: id, Name: "Dispatch media", Chat: up.URL + "/v1", Decide: up.URL + "/v1", Key: "fixture-k1", Keys: []provider.KeyAccount{{Key: "fixture-k2"}}, Models: []string{model}, Routing: provider.Ordered, Affinity: "off"}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	if err := catalog.SaveLive(id, p.Chat, []catalog.Model{{ID: model, Draws: operation == "drawing"}}); err != nil {
		t.Fatal(err)
	}
	h := New().Front()
	send := func(ctx context.Context) *httptest.ResponseRecorder {
		path, body := "/v1/images/generations", `{"model":"`+id+`/`+model+`","prompt":"a cat","response_format":"b64_json"}`
		if operation == "embedding" {
			path = "/v1/embeddings"
			body = `{"model":"` + id + `/` + model + `","input":["hello"]}`
		} else if operation == "decision" {
			path = "/v1/systemone"
			body = `{"model":"` + id + `/` + model + `","state":{"message":"hello"},"questions":{"intent":{"type":"choice"}}}`
		}
		r := httptest.NewRequest("POST", "http://localhost"+path, strings.NewReader(body)).WithContext(ctx)
		r.RemoteAddr = "127.0.0.1:1234"
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	take := func() []routingAliasCall {
		mu.Lock()
		defer mu.Unlock()
		out := slices.Clone(calls)
		calls = nil
		return out
	}
	// Prove both ordinary keys work with this exact operation before any alias
	// or failure is introduced; key order changes do not copy credentials.
	for _, key := range []string{"fixture-k1", "fixture-k2"} {
		if err := provider.UseKey(id, provider.KeyID(key)); err != nil {
			t.Fatal(err)
		}
		w := send(t.Context())
		if w.Code != 200 {
			t.Fatalf("baseline %s key %s: %d %s", operation, key, w.Code, w.Body.String())
		}
		if got := take(); !slices.Equal(got, []routingAliasCall{{key, model}}) {
			t.Fatalf("baseline %s owner/plain model=%v", operation, got)
		}
	}
	if err := provider.UseKey(id, provider.KeyID("fixture-k1")); err != nil {
		t.Fatal(err)
	}
	arm := func() { mu.Lock(); armed = true; mu.Unlock() }
	return id, model, send, take, arm, entered, unblock
}

func TestRoutingAliasMediaRetryRechecksCurrentBinding(t *testing.T) {
	for _, operation := range []string{"drawing", "embedding"} {
		t.Run(operation, func(t *testing.T) {
			id, _, send, take, arm, entered, unblock := aliasMediaSendFixture(t, operation)
			ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
			var wg sync.WaitGroup
			t.Cleanup(func() { cancel(); unblock(); wg.Wait() })
			done := make(chan *httptest.ResponseRecorder, 1)
			arm()
			wg.Add(1)
			go func() { defer wg.Done(); done <- send(ctx) }()
			select {
			case <-entered:
			case w := <-done:
				t.Fatalf("retry fixture ended before first-key barrier: %d %s", w.Code, w.Body.String())
			case <-ctx.Done():
				t.Fatal("first key did not reach controlled barrier")
			}
			if err := provider.SetKeyRoutingAlias(id, provider.KeyID("fixture-k2"), "isolated"); err != nil {
				t.Fatal(err)
			}
			unblock()
			w := <-done
			got := take()
			if w.Code < 400 {
				t.Errorf("%s ordinary retry succeeded after remaining key became isolated: status=%d", operation, w.Code)
			}
			for _, call := range got {
				if call.Key == "fixture-k2" {
					t.Errorf("%s already-planned retry consumed newly isolated key: %v", operation, got)
				}
			}
		})
	}
}

func TestRoutingAliasHeldMediaRejectsCurrentCorruptionWithoutInvalidation(t *testing.T) {
	for _, operation := range []string{"drawing", "embedding"} {
		t.Run(operation, func(t *testing.T) {
			_, _, send, take, _, _, _ := aliasMediaSendFixture(t, operation)
			path := provider.Path()
			original, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path+".bak", original, 0600); err != nil {
				t.Fatal(err)
			}
			settled := time.Now().Add(-time.Hour)
			if err := os.Chtimes(path, settled, settled); err != nil {
				t.Fatal(err)
			}
			releaseHold := filememo.Hold()
			defer releaseHold()
			if err := provider.RoutingFileError(); err != nil {
				t.Fatalf("baseline routing health: %v", err)
			}
			if w := send(t.Context()); w.Code != 200 {
				t.Fatalf("baseline held %s: %d %s", operation, w.Code, w.Body.String())
			}
			take()
			bad := bytes.Repeat([]byte("!"), len(original))
			if err := os.WriteFile(path, bad, 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.Chtimes(path, settled, settled); err != nil {
				t.Fatal(err)
			}
			// No Forget, Touched, generation change, or synthetic cache assertion:
			// only the user-visible request and actual consumption establish health.
			w := send(t.Context())
			got := take()
			if w.Code < 400 {
				t.Errorf("held %s served after same-size current corruption: status=%d", operation, w.Code)
			}
			if len(got) != 0 {
				t.Errorf("held %s consumed upstream with unknown current configuration: %v", operation, got)
			}
		})
	}
}

func TestRoutingAliasSystemOneSendRechecksCurrentBinding(t *testing.T) {
	for _, mode := range []string{"ordinary-isolated", "qualified-retired"} {
		t.Run(mode, func(t *testing.T) {
			id, model, _, take, _, _, _ := aliasMediaSendFixture(t, "decision")
			p, err := provider.Find(id)
			if err != nil {
				t.Fatal(err)
			}
			s := New()
			body := []byte(`{"model":"typesafe/jev","state":{"message":"hello"},"questions":{"intent":{"type":"choice"}}}`)
			if got, err := s.postDecideSent(t.Context(), *p, model, body); err != nil || got.status != 200 {
				t.Fatalf("baseline System One send: status=%d err=%v", got.status, err)
			}
			take()
			snapshot := *p
			if err := provider.SetKeyRoutingAlias(id, provider.KeyID(p.Key), "work"); err != nil {
				t.Fatal(err)
			}
			if mode == "qualified-retired" {
				snapshot, model, err = provider.ResolveModel(id + "/" + model + "@work")
				if err != nil {
					t.Fatal(err)
				}
				if got, err := s.postDecideSent(t.Context(), snapshot, model, body); err != nil || got.status != 200 {
					t.Fatalf("baseline qualified System One send: status=%d err=%v", got.status, err)
				}
				take()
				if err := provider.SetKeyRoutingAlias(id, provider.KeyID(p.Key), ""); err != nil {
					t.Fatal(err)
				}
			}
			res, err := s.postDecideSent(t.Context(), snapshot, model, body)
			got := take()
			if err == nil && res.status < 400 {
				t.Errorf("System One stale %s candidate succeeded: status=%d", mode, res.status)
			}
			if len(got) != 0 {
				t.Errorf("System One stale %s candidate consumed upstream: %v", mode, got)
			}
		})
	}
}
