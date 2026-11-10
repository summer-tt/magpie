package gateway

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/usage"
)

func TestRoutingAliasRemainingProtocolsUseBoundKey(t *testing.T) {
	for _, protocol := range []string{"messages", "gemini", "ollama"} {
		t.Run(protocol, func(t *testing.T) {
			v, h, _ := routingAliasFixture(t)
			send := func(ref string) *httptest.ResponseRecorder {
				path, body := "/v1/messages", `{"model":"`+ref+`","max_tokens":20,"messages":[{"role":"user","content":"hello"}]}`
				if protocol == "gemini" {
					path = "/v1beta/models/" + ref + ":generateContent"
					body = `{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`
				} else if protocol == "ollama" {
					path = "/api/chat"
					body = `{"model":"` + ref + `","stream":false,"messages":[{"role":"user","content":"hello"}]}`
				}
				r := httptest.NewRequest("POST", "http://localhost"+path, strings.NewReader(body))
				r.RemoteAddr = "127.0.0.1:1234"
				w := httptest.NewRecorder()
				h.ServeHTTP(w, r)
				return w
			}
			if w := send("alias-relay/m"); w.Code != 200 {
				t.Fatalf("baseline %s=%d %s", protocol, w.Code, w.Body.String())
			}
			if got := v.take(); !slices.Equal(got, []routingAliasCall{{"k-ordinary", "m"}}) {
				t.Fatalf("baseline %s wire=%v", protocol, got)
			}
			if w := send("alias-relay/m@work"); w.Code != 200 {
				t.Errorf("qualified %s=%d %s", protocol, w.Code, w.Body.String())
			}
			if got := v.take(); !slices.Equal(got, []routingAliasCall{{"k-work", "m"}}) {
				t.Errorf("qualified %s target/plain wire=%v", protocol, got)
			}
		})
	}
}
func TestRoutingAliasRemoteKeyKeepsNativeAtModel(t *testing.T) {
	fresh(t)
	var mu sync.Mutex
	var calls []routingAliasCall
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/models") {
			io.WriteString(w, `{"data":[{"id":"vendor/m@500k","native_endpoints":["/v1/chat/completions"]}]}`)
			return
		}
		if r.Method == "GET" {
			io.WriteString(w, `{"name":"magpie"}`)
			return
		}
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		calls = append(calls, routingAliasCall{strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), modelOf(b)})
		mu.Unlock()
		io.WriteString(w, `{"choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}]}`)
	}))
	t.Cleanup(remote.Close)
	id, err := provider.Add(provider.Provider{ID: "remote-alias", Name: "Remote alias", Preset: provider.RemoteMagpiePreset, Key: "remote-ordinary", Keys: []provider.KeyAccount{{Key: "remote-work"}}, Chat: remote.URL})
	if err != nil {
		t.Fatal(err)
	}
	p, err := provider.Find(id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = p.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	take := func() []routingAliasCall {
		mu.Lock()
		defer mu.Unlock()
		out := slices.Clone(calls)
		calls = nil
		return out
	}
	h := New().Front()
	if w := routingAliasSend(h, id+"/vendor/m@500k", ""); w.Code != 200 {
		t.Fatalf("baseline remote=%d %s", w.Code, w.Body.String())
	}
	if got := take(); !slices.Equal(got, []routingAliasCall{{"remote-ordinary", "vendor/m@500k"}}) {
		t.Fatalf("baseline remote wire=%v", got)
	}
	if err := provider.SetKeyRoutingAlias(id, provider.KeyID("remote-work"), "work"); err != nil {
		t.Fatal(err)
	}
	if w := routingAliasSend(h, id+"/vendor/m@500k@work", ""); w.Code != 200 {
		t.Errorf("qualified remote=%d %s", w.Code, w.Body.String())
	}
	if got := take(); !slices.Equal(got, []routingAliasCall{{"remote-work", "vendor/m@500k"}}) {
		t.Errorf("remote local alias altered native model/auth=%v", got)
	}
	if w := routingAliasSend(h, id+"/vendor/m@500k@absent", ""); w.Code != 404 {
		t.Errorf("unknown local alias remote status=%d", w.Code)
	}
	if got := take(); len(got) != 0 {
		t.Errorf("unknown local alias forwarded to remote=%v", got)
	}
}
func TestRoutingAliasVideoResourceKeepsCreatingKey(t *testing.T) {
	fresh(t)
	up := newOpenAIVideos(t)
	saveFetched(t, provider.Provider{ID: "alias-video", Name: "Alias video", Chat: up.URL + "/v1", Key: "video-work", Keys: []provider.KeyAccount{{Key: "video-other"}}})
	h := New().Front()
	call := func(method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "http://localhost"+path, strings.NewReader(body))
		r.RemoteAddr = "127.0.0.1:1234"
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	create := func(ref string) string {
		t.Helper()
		w := call("POST", "/v1/videos", `{"model":"`+ref+`","prompt":"a cat","seconds":4,"size":"1280x720"}`)
		var obj map[string]any
		json.Unmarshal(w.Body.Bytes(), &obj)
		id, _ := obj["id"].(string)
		if w.Code != 200 || id == "" {
			t.Fatalf("video create status=%d %s", w.Code, w.Body.String())
		}
		return id
	}
	create("alias-video/vid-1")
	if err := provider.SetKeyRoutingAlias("alias-video", provider.KeyID("video-work"), "work"); err != nil {
		t.Fatal(err)
	}
	id := create("alias-video/vid-1@work")
	if err := provider.SetKeyRoutingAlias("alias-video", provider.KeyID("video-work"), "renamed"); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"", "/content"} {
		w := call("GET", "/v1/videos/"+id+suffix, "")
		if w.Code != 200 {
			t.Errorf("existing resource after rename status=%d suffix=%s", w.Code, suffix)
		}
	}
	for _, entry := range up.log() {
		if strings.HasPrefix(entry, "GET /v1/videos/") && !strings.HasSuffix(entry, " video-work") {
			t.Errorf("video resource changed creating key=%s", entry)
		}
	}
	if err := provider.RemoveKey("alias-video", provider.KeyID("video-work")); err != nil {
		t.Fatal(err)
	}
	before := len(up.log())
	for _, suffix := range []string{"", "/content"} {
		w := call("GET", "/v1/videos/"+id+suffix, "")
		if w.Code < 400 {
			t.Errorf("removed video key resource silently switched status=%d", w.Code)
		}
	}
	if len(up.log()) != before {
		t.Error("removed video key resource consumed another key")
	}
}
func TestRoutingAliasDrawingGroupKeepsTwoExplicitOwners(t *testing.T) {
	fresh(t)
	var mu sync.Mutex
	var calls []routingAliasCall
	refuse := false
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b struct {
			Model string `json:"model"`
		}
		json.NewDecoder(r.Body).Decode(&b)
		key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		mu.Lock()
		calls = append(calls, routingAliasCall{key, b.Model})
		limited := refuse && key == "image-a"
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if limited {
			w.WriteHeader(429)
			io.WriteString(w, `{"error":{"message":"target rate limit"}}`)
			return
		}
		io.WriteString(w, `{"created":1,"data":[{"b64_json":"`+base64.StdEncoding.EncodeToString(pngBytes)+`"}]}`)
	}))
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{ID: "alias-art", Name: "Alias art", Chat: up.URL + "/v1", Key: "image-a", Keys: []provider.KeyAccount{{Key: "image-b"}}, Models: []string{"gpt-image-1"}}); err != nil {
		t.Fatal(err)
	}
	for _, pair := range []struct{ key, alias string }{{"image-a", "a"}, {"image-b", "b"}} {
		if err := provider.SetKeyRoutingAlias("alias-art", provider.KeyID(pair.key), pair.alias); err != nil {
			t.Fatal(err)
		}
	}
	s := New()
	if code, _, raw := postImages(t, s, "/v1/images/generations", "application/json", `{"model":"alias-art/gpt-image-1@a","prompt":"a cat","response_format":"b64_json"}`); code != 200 {
		t.Fatalf("baseline qualified drawing=%d %s", code, raw)
	}
	mu.Lock()
	calls = nil
	refuse = true
	mu.Unlock()
	if err := provider.SaveGroup(provider.Group{ID: "alias-draw", Name: "Alias draw", Members: []string{"alias-art/gpt-image-1@a", "alias-art/gpt-image-1@b"}, Routing: provider.Ordered}); err != nil {
		t.Fatal(err)
	}
	code, _, _ := postImages(t, s, "/v1/images/generations", "application/json", `{"model":"group/alias-draw","prompt":"a cat","response_format":"b64_json"}`)
	mu.Lock()
	got := slices.Clone(calls)
	mu.Unlock()
	if code != 200 || !slices.Contains(got, routingAliasCall{"image-b", "gpt-image-1"}) {
		t.Errorf("explicit second drawing owner lost: status=%d calls=%v", code, got)
	}
	for _, c := range got {
		if c.Model != "gpt-image-1" || c.Key != "image-a" && c.Key != "image-b" {
			t.Errorf("drawing group escaped explicit identity=%v", got)
		}
	}
}
func TestRoutingAliasEmptyDrawingAndOnKeysNeverRestoreProvider(t *testing.T) {
	t.Run("drawing", func(t *testing.T) {
		s, e := easeled(t)
		if code, _, raw := postImages(t, s, "/v1/images/generations", "application/json", `{"model":"art/gpt-image-1","prompt":"a cat"}`); code != 200 {
			t.Fatalf("baseline drawing=%d %s", code, raw)
		}
		before := len(e.got("/v1/images/generations"))
		if err := provider.SetKeyRoutingAlias("art", provider.KeyID("key"), "work"); err != nil {
			t.Fatal(err)
		}
		code, _, _ := postImages(t, s, "/v1/images/generations", "application/json", `{"model":"art/gpt-image-1","prompt":"a cat"}`)
		if code < 400 || len(e.got("/v1/images/generations")) != before {
			t.Errorf("empty drawing pool restored isolated key: status=%d", code)
		}
	})
	t.Run("onKeys", func(t *testing.T) {
		s, l := shelved(t)
		if code, raw := postRetrieval(t, s, "/v1/embeddings", `{"model":"lib/embed-1","input":["hello"]}`); code != 200 {
			t.Fatalf("baseline embeddings=%d %s", code, raw)
		}
		before := len(l.got("/v1/embeddings"))
		if err := provider.SetKeyRoutingAlias("lib", provider.KeyID("key"), "work"); err != nil {
			t.Fatal(err)
		}
		code, _ := postRetrieval(t, s, "/v1/embeddings", `{"model":"lib/embed-1","input":["hello"]}`)
		if code < 400 || len(l.got("/v1/embeddings")) != before {
			t.Errorf("empty onKeys restored isolated key: status=%d", code)
		}
	})
}
func TestRoutingAliasUsageHeadersPriceAndOptionalOTLP(t *testing.T) {
	v, h, _ := routingAliasFixture(t)
	set := catalog.Price{Input: 3, Output: 12}
	if err := provider.SetModelPrice("alias-relay/m", &set); err != nil {
		t.Fatal(err)
	}
	received := make(chan []byte, 64)
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		received <- b
		io.WriteString(w, `{}`)
	}))
	defer collector.Close()
	t.Setenv("MAGPIE_OTEL_ENABLED", "true")
	t.Setenv("MAGPIE_OTEL_ENDPOINT", collector.URL)
	t.Setenv("MAGPIE_OTEL_METRICS", "false")
	t.Setenv("MAGPIE_OTEL_SESSIONS", "false")
	t.Setenv("MAGPIE_OTEL_HEADERS", "")
	fastOTel(t)
	stop := startOTel()
	stopped := false
	defer func() {
		if !stopped {
			stop()
		}
	}()
	w := routingAliasSend(h, "alias-relay/m@work", "")
	if w.Code != 200 {
		t.Fatalf("qualified record request=%d", w.Code)
	}
	if got := v.take(); !slices.Equal(got, []routingAliasCall{{"k-work", "m"}}) {
		t.Fatalf("record target/plain model=%v", got)
	}
	if got := w.Header().Get("X-Magpie-Model"); got != "alias-relay/m" {
		t.Errorf("actual model header=%q want alias-relay/m", got)
	}
	if got := w.Header().Get("X-Magpie-Provider"); got != "alias-relay" {
		t.Errorf("provider header=%q", got)
	}
	found := false
	for _, r := range usage.Load(time.Time{}) {
		if r.Requested == "alias-relay/m@work" {
			found = true
			if r.Model != "m" || r.ProviderKeyID != provider.KeyID("k-work") {
				t.Errorf("usage lost plain model/original key: model=%q key=%q", r.Model, r.ProviderKeyID)
			}
		}
	}
	if !found {
		t.Error("usage missing complete Requested alias")
	}
	entries := map[string]provider.Entry{}
	for _, e := range provider.Catalog() {
		entries[e.ID] = e
	}
	for _, id := range []string{"alias-relay/m", "alias-relay/m@work"} {
		pr, ok := provider.EntryPriceIn(settings.Load(), provider.FindGroup, entries[id])
		if !ok || !pr.Same(set) {
			t.Errorf("price %s=%+v ok=%v want base price", id, pr, ok)
		}
	}
	stop()
	stopped = true
	traces, _ := otelExports(received)
	joined := strings.Builder{}
	for _, b := range traces {
		joined.Write(b)
	}
	if !strings.Contains(joined.String(), "magpie.requested_model") || !strings.Contains(joined.String(), "alias-relay/m@work") {
		t.Error("enabled local OTLP collector lost existing Requested attribute")
	}
}

// Account restrictions can leave only the second alias eligible. Drawing must
// keep the planner's qualified reference when mapping it back to a member.
func TestRoutingAliasDrawingOrderKeepsAccountIntersection(t *testing.T) {
	_, _, update := routingAliasFixture(t)
	update(map[string]any{"a": routingAliasBinding("k-work", true), "b": routingAliasBinding("k-ordinary", true)})
	if err := provider.SaveGroup(provider.Group{ID: "alias-drawing-order", Name: "Alias drawing order", Members: []string{"alias-relay/m@a#" + provider.KeyID("k-ordinary"), "alias-relay/m@b#" + provider.KeyID("k-ordinary")}, Routing: provider.Ordered}); err != nil {
		t.Fatal(err)
	}
	g, ms, ok := provider.FindGroup("group/alias-drawing-order")
	if !ok || len(ms) != 2 {
		t.Fatalf("group members: ok=%v members=%v", ok, ms)
	}
	got := New().drawingTries(g, ms, nil, func(provider.Provider, string) bool { return true })
	if len(got) != 2 || got[0].Provider.ModelRef(got[0].Model) != "alias-relay/m@b" {
		var refs []string
		for _, m := range got {
			refs = append(refs, m.Provider.ModelRef(m.Model))
		}
		t.Fatalf("eligible drawing member must lead: %v", refs)
	}
}
