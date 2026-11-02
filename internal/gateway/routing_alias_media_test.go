package gateway

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
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
)

// Each row is an independent gateway entry that selects keys. The fixture
// returns that protocol's normal successful response before isolation is set.
func TestRoutingAliasIndependentServiceEntries(t *testing.T) {
	for _, operation := range []string{"embedding", "rerank", "image-generation", "image-edit", "video", "transcription", "decision"} {
		t.Run(operation, func(t *testing.T) {
			fresh(t)
			var mu sync.Mutex
			var calls []routingAliasCall
			refuse := false
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/models") {
					io.WriteString(w, `{"data":[{"id":"embed-1"},{"id":"rerank-1"},{"id":"gpt-image-1","kind":"image"},{"id":"vid-1","kind":"video"},{"id":"whisper-1"},{"id":"typesafe/jev"}]}`)
					return
				}
				var model string
				if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
					if err := r.ParseMultipartForm(32 << 20); err != nil {
						http.Error(w, err.Error(), 400)
						return
					}
					defer r.MultipartForm.RemoveAll()
					model = r.FormValue("model")
				} else {
					var body struct {
						Model string `json:"model"`
					}
					json.NewDecoder(r.Body).Decode(&body)
					model = body.Model
				}
				key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
				mu.Lock()
				calls = append(calls, routingAliasCall{key, model})
				limited := refuse && key == "key-isolated"
				mu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				if limited {
					w.WriteHeader(429)
					io.WriteString(w, `{"error":{"message":"target quota","type":"rate_limit_error"}}`)
					return
				}
				switch r.URL.Path {
				case "/v1/embeddings":
					io.WriteString(w, `{"object":"list","model":"embed-1","data":[{"object":"embedding","index":0,"embedding":[0.1]}],"usage":{"prompt_tokens":2,"total_tokens":2}}`)
				case "/v1/rerank":
					io.WriteString(w, `{"model":"rerank-1","results":[{"index":0,"relevance_score":0.9}],"usage":{"total_tokens":2}}`)
				case "/v1/images/generations", "/v1/images/edits":
					io.WriteString(w, `{"created":1,"data":[{"b64_json":"`+base64.StdEncoding.EncodeToString(pngBytes)+`"}]}`)
				case "/v1/videos":
					io.WriteString(w, `{"id":"video_fixture","object":"video","status":"queued","model":"vid-1","seconds":"4","size":"1280x720"}`)
				case "/v1/audio/transcriptions":
					io.WriteString(w, `{"text":"fixture transcript","usage":{"type":"tokens","input_tokens":2,"output_tokens":2,"total_tokens":4}}`)
				case "/v1/systemone":
					io.WriteString(w, `{"model":"typesafe/jev","answers":{"intent":{"type":"choice","choice":"bug","confidence":0.9},"levels":{"type":"noul","noul":0}},"usage":{"input_tokens":2,"output_tokens":2}}`)
				default:
					http.NotFound(w, r)
				}
			}))
			t.Cleanup(up.Close)
			p := provider.Provider{ID: "service-alias", Name: "Service alias", Chat: up.URL + "/v1", Decide: up.URL + "/v1", Key: "key-isolated", Keys: []provider.KeyAccount{{Key: "key-ordinary"}}, Models: []string{"embed-1", "rerank-1", "gpt-image-1", "vid-1", "whisper-1", "typesafe/jev"}}
			if err := provider.Save(p); err != nil {
				t.Fatal(err)
			}
			if err := catalog.SaveLive(p.ID, p.Chat, []catalog.Model{{ID: "embed-1"}, {ID: "rerank-1"}, {ID: "gpt-image-1", Draws: true}, {ID: "vid-1", Films: true}, {ID: "whisper-1"}, {ID: "typesafe/jev"}}); err != nil {
				t.Fatal(err)
			}
			s := New()
			h := s.Front()
			model := map[string]string{"embedding": "embed-1", "rerank": "rerank-1", "image-generation": "gpt-image-1", "image-edit": "gpt-image-1", "video": "vid-1", "transcription": "whisper-1", "decision": "typesafe/jev"}[operation]
			send := func(ref string) (int, string) {

				path, ctype, body := "", "application/json", []byte(nil)
				switch operation {
				case "decision":
					path = "/v1/systemone"
					body = []byte(`{"model":"` + ref + `","state":{"message":"fix this"},"questions":{"intent":{"type":"choice"}}}`)
				case "embedding":
					path = "/v1/embeddings"
					body = []byte(`{"model":"` + ref + `","input":["hello"]}`)
				case "rerank":
					path = "/v1/rerank"
					body = []byte(`{"model":"` + ref + `","query":"hi","documents":["hello"]}`)
				case "image-generation":
					path = "/v1/images/generations"
					body = []byte(`{"model":"` + ref + `","prompt":"a cat","response_format":"b64_json"}`)
				case "image-edit":
					path = "/v1/images/edits"
					body = []byte(`{"model":"` + ref + `","prompt":"a cat","image":"data:image/png;base64,` + base64.StdEncoding.EncodeToString(pngBytes) + `","response_format":"b64_json"}`)
				case "video":
					path = "/v1/videos"
					body = []byte(`{"model":"` + ref + `","prompt":"a cat","seconds":4,"size":"1280x720"}`)
				case "transcription":
					path = "/v1/audio/transcriptions"
					body, ctype = audioFormBody(t, "model", ref, "response_format", "json")
				}
				r := httptest.NewRequest("POST", "http://localhost"+path, bytes.NewReader(body))
				r.RemoteAddr = "127.0.0.1:12345"
				r.Header.Set("Content-Type", ctype)
				w := httptest.NewRecorder()
				h.ServeHTTP(w, r)
				return w.Code, w.Body.String()
			}
			take := func() []routingAliasCall {
				mu.Lock()
				defer mu.Unlock()
				got := slices.Clone(calls)
				calls = nil
				return got
			}
			if code, body := send(p.ID + "/" + model); code != 200 {
				t.Fatalf("baseline %s: %d %s", operation, code, body)
			}
			baselineCalls := take()
			if operation == "decision" {
				if len(baselineCalls) == 0 {
					t.Fatal("baseline decision never reached upstream")
				}
				for _, c := range baselineCalls {
					if c.Key != "key-isolated" || c.Model != model {
						t.Fatalf("baseline decision wrong owner/model=%v", baselineCalls)
					}
				}
			} else if !slices.Equal(baselineCalls, []routingAliasCall{{"key-isolated", model}}) {
				t.Fatalf("baseline %s calls=%v", operation, baselineCalls)
			}
			// Inject through the saved shape, independently of the new alias setter.
			var cfg map[string]any
			raw, err := json.Marshal(p)
			if err != nil {
				t.Fatal(err)
			}
			if err = json.Unmarshal(raw, &cfg); err != nil {
				t.Fatal(err)
			}
			cfg["keyAliases"] = map[string]any{"work": map[string]any{"ownerId": provider.KeyID("key-isolated"), "keyId": provider.KeyID("key-isolated"), "active": true}}
			putProvider(t, cfg)
			filememo.Forget()
			catalog.Touched()
			if code, body := send(p.ID + "/" + model); code != 200 {
				t.Errorf("ordinary %s: %d %s", operation, code, body)
			}
			ordinaryCalls := take()
			if operation == "decision" {
				if len(ordinaryCalls) == 0 {
					t.Error("ordinary decision never reached upstream")
				}
				for _, c := range ordinaryCalls {
					if c.Key != "key-ordinary" || c.Model != model {
						t.Errorf("ordinary decision consumed wrong owner/model=%v", ordinaryCalls)
					}
				}
			} else if !slices.Equal(ordinaryCalls, []routingAliasCall{{"key-ordinary", model}}) {
				t.Errorf("ordinary %s consumed wrong owner/model=%v", operation, ordinaryCalls)
			}
			if code, body := send(p.ID + "/" + model + "@work"); code != 200 {
				t.Errorf("qualified %s: %d %s", operation, code, body)
			}
			qualifiedCalls := take()
			if operation == "decision" {
				if len(qualifiedCalls) == 0 {
					t.Error("qualified decision never reached upstream")
				}
				for _, c := range qualifiedCalls {
					if c.Key != "key-isolated" || c.Model != model {
						t.Errorf("qualified decision consumed wrong owner/model=%v", qualifiedCalls)
					}
				}
			} else if !slices.Equal(qualifiedCalls, []routingAliasCall{{"key-isolated", model}}) {
				t.Errorf("qualified %s consumed wrong owner/model=%v", operation, qualifiedCalls)
			}
			mu.Lock()
			refuse = true
			mu.Unlock()
			if code, _ := send(p.ID + "/" + model + "@work"); code < 400 {
				t.Errorf("failed qualified %s reported success=%d", operation, code)
			}
			got := take()
			if len(got) == 0 {
				t.Errorf("qualified %s failure never reached known target", operation)
			}
			for _, c := range got {
				if c.Key != "key-isolated" || c.Model != model {
					t.Errorf("qualified %s failure escaped owner: %v", operation, got)
				}
			}
		})
	}
}
