package gateway

import (
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

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/filememo"
	"github.com/yetone/magpie/internal/provider"
)

func TestRoutingAliasMediaRejectsDamagedCurrentDespiteOldBackup(t *testing.T) {
	for _, operation := range []string{"drawing", "embedding"} {
		t.Run(operation, func(t *testing.T) {
			fresh(t)
			var mu sync.Mutex
			var calls []routingAliasCall
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if strings.HasSuffix(r.URL.Path, "/models") {
					io.WriteString(w, `{"data":[{"id":"gpt-image-1","kind":"image"},{"id":"embed-1"}]}`)
					return
				}
				var body struct {
					Model string `json:"model"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					http.Error(w, "invalid fixture body", 400)
					return
				}
				mu.Lock()
				calls = append(calls, routingAliasCall{r.Header.Get("Authorization"), body.Model})
				mu.Unlock()
				switch r.URL.Path {
				case "/v1/images/generations":
					io.WriteString(w, `{"created":1,"data":[{"b64_json":"`+base64.StdEncoding.EncodeToString(pngBytes)+`"}]}`)
				case "/v1/embeddings":
					io.WriteString(w, `{"object":"list","model":"embed-1","data":[{"object":"embedding","index":0,"embedding":[0.1]}],"usage":{"prompt_tokens":2,"total_tokens":2}}`)
				default:
					http.NotFound(w, r)
				}
			}))
			t.Cleanup(up.Close)
			p := provider.Provider{ID: "health-media", Name: "Health media", Chat: up.URL + "/v1", Key: "fixture-media-key", Models: []string{"gpt-image-1", "embed-1"}}
			if err := provider.Save(p); err != nil {
				t.Fatal(err)
			}
			if err := catalog.SaveLive(p.ID, p.Chat, []catalog.Model{{ID: "gpt-image-1", Draws: true}, {ID: "embed-1"}}); err != nil {
				t.Fatal(err)
			}
			h := New().Front()
			model := "gpt-image-1"
			if operation == "embedding" {
				model = "embed-1"
			}
			send := func(qualifier string) *httptest.ResponseRecorder {
				path, body := "/v1/images/generations", `{"model":"health-media/`+model+qualifier+`","prompt":"a cat","response_format":"b64_json"}`
				if operation == "embedding" {
					path = "/v1/embeddings"
					body = `{"model":"health-media/` + model + qualifier + `","input":["hello"]}`
				}
				r := httptest.NewRequest("POST", "http://localhost"+path, strings.NewReader(body))
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
			if w := send(""); w.Code != 200 {
				t.Fatalf("baseline ordinary %s: %d %s", operation, w.Code, w.Body.String())
			}
			if got := take(); !slices.Equal(got, []routingAliasCall{{"Bearer fixture-media-key", model}}) {
				t.Fatalf("baseline ordinary media target=%v", got)
			}
			old, err := os.ReadFile(provider.Path())
			if err != nil {
				t.Fatal(err)
			}
			if err := provider.SetKeyRoutingAlias(p.ID, provider.KeyID(p.Key), "work"); err != nil {
				t.Fatal(err)
			}
			if w := send("@work"); w.Code != 200 {
				t.Fatalf("baseline qualified %s: %d %s", operation, w.Code, w.Body.String())
			}
			if got := take(); !slices.Equal(got, []routingAliasCall{{"Bearer fixture-media-key", model}}) {
				t.Fatalf("baseline qualified media target=%v", got)
			}
			// The valid old backup predates isolation. It may support display recovery,
			// but cannot establish the current account's ordinary routing eligibility.
			if err := os.WriteFile(provider.Path()+".bak", old, 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(provider.Path(), []byte(`{"providers":[`), 0600); err != nil {
				t.Fatal(err)
			}
			filememo.Forget()
			catalog.Touched()
			w := send("")
			got := take()
			if w.Code < 400 {
				t.Errorf("%s accepted unknown current isolation via old backup: status=%d", operation, w.Code)
			}
			if len(got) != 0 {
				t.Errorf("%s consumed restored key while current config was unreadable: %v", operation, got)
			}
		})
	}
}
