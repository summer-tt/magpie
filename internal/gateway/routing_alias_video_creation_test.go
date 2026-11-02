package gateway

import (
	"bytes"
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

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

// startVideo picks among a provider's keys only at a provider's own
// videos API (a custom one, MiniMax) or a mixed-plan one; another magpie
// and a single-plan preset like Volcengine send on the provider's first
// key as it is, so a routing qualifier naming another key, or an isolated
// first key, meets the send-time fresh guard instead of the key it
// names. These tests run the real POST /v1/videos path against a fake
// remote magpie and a fake Volcengine on loopback, and say which key each
// video POST left on and with which model in its body. No real remote,
// Volcengine address or credential is involved.

// routingAliasVideoPost is one video POST as it left this magpie.
type routingAliasVideoPost struct {
	bearer, body string
}

// routingAliasVideoCreatePaths is whether a POST path starts a video: the
// OpenAI-shaped videos API wherever it sits ("/videos", "/v1/videos") or a
// Grok-style "/videos/generations". Model-list GETs are not creates.
func routingAliasIsVideoCreate(path string) bool {
	return strings.HasSuffix(path, "/videos") || strings.HasSuffix(path, "/videos/generations")
}

// routingAliasVideoAsks keeps the asks that touch the videos API — the
// create, the polls and the content — leaving model-list metadata out, so a
// key's model discovery is never counted as a video's consumption.
func routingAliasVideoAsks(asked []string) []string {
	var out []string
	for _, a := range asked {
		if strings.Contains(a, "/videos") {
			out = append(out, a)
		}
	}
	return out
}

// routingAliasRemoteEdge is a fake remote magpie — a real gateway handler
// over the Grok media fake, as TestRemoteMagpieMedia's — with this
// magpie's asks recorded: each request's method, path and bearer key, and
// the body of each video-starting POST, whichever of its paths it took.
type routingAliasRemoteEdge struct {
	*httptest.Server
	mu    sync.Mutex
	asked []string
	posts []routingAliasVideoPost
}

func newRoutingAliasRemoteMagpie(t *testing.T) *routingAliasRemoteEdge {
	t.Helper()
	grokSignedIn(t)
	newGrokMedia(t)
	// a local catalog, as other fixtures seed one: without it the sandbox
	// has no models.dev cache and a catalog sync reaches for the network,
	// which testenv refuses; an empty one is no catalog data at all
	if err := os.MkdirAll(filepath.Dir(catalog.CachePath()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(catalog.CachePath(), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	inner := New().Handler()
	innerSrv := httptest.NewServer(inner)
	t.Cleanup(innerSrv.Close)
	e := &routingAliasRemoteEdge{}
	e.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bearer := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		var body []byte
		if r.Method == http.MethodPost {
			body, _ = io.ReadAll(r.Body)
			r.Body = io.NopCloser(bytes.NewReader(body))
		}
		e.mu.Lock()
		e.asked = append(e.asked, r.Method+" "+r.URL.Path+" "+bearer)
		if body != nil && routingAliasIsVideoCreate(r.URL.Path) {
			e.posts = append(e.posts, routingAliasVideoPost{bearer, string(body)})
		}
		e.mu.Unlock()
		inner.ServeHTTP(w, r)
	}))
	t.Cleanup(e.Close)
	return e
}

// take hands out the asks since the last take.
func (e *routingAliasRemoteEdge) take() ([]string, []routingAliasVideoPost) {
	e.mu.Lock()
	defer e.mu.Unlock()
	asked, posts := slices.Clone(e.asked), slices.Clone(e.posts)
	e.asked, e.posts = nil, nil
	return asked, posts
}

// routingAliasSaveRemote saves the two-key remote-magpie provider on the
// fake remote and fetches its model list, as adding it does.
func routingAliasSaveRemote(t *testing.T, e *routingAliasRemoteEdge) {
	t.Helper()
	if err := provider.Save(provider.Provider{ID: "office", Name: "Office", Preset: provider.RemoteMagpiePreset,
		Chat: e.URL, Key: "rk-1", Keys: []provider.KeyAccount{{Key: "rk-2"}}}); err != nil {
		t.Fatal(err)
	}
	p, err := provider.Find("office")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// routingAliasCheckPOST says whether the asks since the last take were
// exactly one video-starting POST on wantKey with the plain upstream model
// in its body.
func routingAliasCheckPOST(t *testing.T, e *routingAliasRemoteEdge, wantKey, wantModel string) {
	t.Helper()
	asked, posts := e.take()
	if len(posts) != 1 || posts[0].bearer != wantKey {
		t.Errorf("video POSTs = %+v (asks %v); want exactly one signed with %s", posts, asked, wantKey)
		return
	}
	var sent map[string]any
	if err := json.Unmarshal([]byte(posts[0].body), &sent); err != nil || sent["model"] != wantModel {
		t.Errorf("video POST body = %s; want the plain upstream model %q", posts[0].body, wantModel)
	}
}

// A video at another magpie spends the key the routing state allows: the
// ordinary reference the ordinary keys, a qualified one its bound key,
// and none at all when every key is isolated.
func TestRoutingAliasRemoteMagpieVideoCreationKeys(t *testing.T) {
	e := newRoutingAliasRemoteMagpie(t)
	routingAliasSaveRemote(t, e)
	h := New().Handler()
	post := func(model string) (int, map[string]any, []byte) {
		return asKey(t, h, "", "POST", "/v1/videos", `{"model":"`+model+`","prompt":"a kite"}`)
	}
	// positive baseline: with no alias the video starts through the first
	// key, its body naming the plain model
	code, obj, raw := post("office/grok/grok-imagine-video")
	if code != 200 || obj["id"] == nil {
		t.Fatalf("baseline video: %d %s", code, raw)
	}
	routingAliasCheckPOST(t, e, "rk-1", "grok/grok-imagine-video")

	// the first key isolated: the ordinary reference must spend the
	// second ordinary key, and nothing else
	if err := provider.SetKeyRoutingAlias("office", provider.KeyID("rk-1"), "home"); err != nil {
		t.Fatal(err)
	}
	code, obj, raw = post("office/grok/grok-imagine-video")
	if code != 200 || obj["id"] == nil {
		t.Errorf("ordinary video with the first key isolated: %d %s; want it made on the second ordinary key", code, raw)
	}
	routingAliasCheckPOST(t, e, "rk-2", "grok/grok-imagine-video")

	// a qualifier naming the second key: the POST must go out signed with
	// that key, its body still the plain model
	if err := provider.SetKeyRoutingAlias("office", provider.KeyID("rk-2"), "work"); err != nil {
		t.Fatal(err)
	}
	code, obj, raw = post("office/grok/grok-imagine-video@work")
	if code != 200 || obj["id"] == nil {
		t.Errorf("qualified video on the bound key: %d %s; want 200 on the bound key, not a refusal", code, raw)
	}
	routingAliasCheckPOST(t, e, "rk-2", "grok/grok-imagine-video")

	// every key isolated: an ordinary reference consumes nothing
	code, _, raw = post("office/grok/grok-imagine-video")
	if code < 400 {
		t.Errorf("ordinary video with every key isolated: %d; want a refusal", code)
	}
	if asked, _ := e.take(); len(asked) != 0 {
		t.Errorf("ordinary video with every key isolated reached the remote: %v", asked)
	}
}

// routingAliasVolcEdge is a fake Volcengine Agent Plan, as
// TestVolcengineVideoIsStartedPolledAndFetched's, with the bearer of each
// ask at its tasks API recorded.
type routingAliasVolcEdge struct {
	*httptest.Server
	mu    sync.Mutex
	asked []string
	posts []routingAliasVideoPost
	polls int
}

func (e *routingAliasVolcEdge) take() ([]string, []routingAliasVideoPost) {
	e.mu.Lock()
	defer e.mu.Unlock()
	asked, posts := slices.Clone(e.asked), slices.Clone(e.posts)
	e.asked, e.posts = nil, nil
	return asked, posts
}

func newRoutingAliasVolcVideo(t *testing.T) *routingAliasVolcEdge {
	t.Helper()
	fresh(t)
	e := &routingAliasVolcEdge{}
	var up *httptest.Server
	up = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bearer := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		var body []byte
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/contents/generations/tasks") {
			body, _ = io.ReadAll(r.Body)
			r.Body = io.NopCloser(bytes.NewReader(body))
		}
		e.mu.Lock()
		e.asked = append(e.asked, r.Method+" "+r.URL.Path+" "+bearer)
		if body != nil {
			e.posts = append(e.posts, routingAliasVideoPost{bearer, string(body)})
		}
		e.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/contents/generations/tasks"):
			io.WriteString(w, volcengineVideoCreateResponse)
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/tasks/cgt-20261008165954-4sc9n"):
			e.mu.Lock()
			e.polls++
			n := e.polls
			e.mu.Unlock()
			if n == 1 {
				io.WriteString(w, volcengineVideoRunningResponse)
			} else {
				io.WriteString(w, strings.Replace(volcengineVideoSucceededResponse, "VIDEO_URL", up.URL+"/ark/video.mp4", 1))
			}
		case r.URL.Path == "/ark/video.mp4":
			w.Header().Set("Content-Type", "video/mp4")
			io.WriteString(w, "ark-video-bytes")
		default:
			w.WriteHeader(404)
			io.WriteString(w, `{"error":{"code":"not_found","message":"unexpected path"}}`)
		}
	}))
	t.Cleanup(up.Close)
	e.Server = up
	p, err := provider.FromPreset("volcengine")
	if err != nil {
		t.Fatal(err)
	}
	p.ID, p.Key = "ark", "ark-a"
	p.Chat, p.Responses = up.URL+"/api/plan/v3", up.URL+"/api/plan/v3"
	p.Anthropic = up.URL + "/api/plan"
	p.Keys = []provider.KeyAccount{{Key: "ark-b"}}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	if ms := Videomakers(mustFind(t, "ark")); len(ms) == 0 {
		t.Fatal("the fake serves no plan videos; its paths must match the preset's Agent Plan region")
	}
	return e
}

// routingAliasCheckVolcPOST is routingAliasCheckPOST for the Volcengine
// tasks API.
func routingAliasCheckVolcPOST(t *testing.T, e *routingAliasVolcEdge, wantKey string) {
	t.Helper()
	_, posts := e.take()
	if len(posts) != 1 || posts[0].bearer != wantKey {
		t.Errorf("video POSTs = %+v; want exactly one signed with %s", posts, wantKey)
		return
	}
	var sent map[string]any
	if err := json.Unmarshal([]byte(posts[0].body), &sent); err != nil || sent["model"] != "doubao-seedance-2.0-mini" {
		t.Errorf("video POST body = %s; want the plain upstream model", posts[0].body)
	}
}

// A video at a single-plan Volcengine spends the key the routing state
// allows, as another magpie's does.
func TestRoutingAliasVolcengineVideoCreationKeys(t *testing.T) {
	e := newRoutingAliasVolcVideo(t)
	h := New().Handler()
	post := func(model string) (int, map[string]any, []byte) {
		return asKey(t, h, "", "POST", "/v1/videos", `{"model":"`+model+`","prompt":"a magpie","seconds":5,"size":"854x480"}`)
	}
	// positive baseline: with no alias the video starts on the first key
	code, obj, raw := post("ark/doubao-seedance-2.0-mini")
	if code != 200 || obj["status"] != "queued" {
		t.Fatalf("baseline video: %d %s", code, raw)
	}
	routingAliasCheckVolcPOST(t, e, "ark-a")

	// the first key isolated: the ordinary reference must spend the
	// second ordinary key
	if err := provider.SetKeyRoutingAlias("ark", provider.KeyID("ark-a"), "home"); err != nil {
		t.Fatal(err)
	}
	code, obj, raw = post("ark/doubao-seedance-2.0-mini")
	if code != 200 || obj["status"] != "queued" {
		t.Errorf("ordinary video with the first key isolated: %d %s; want it made on the second ordinary key", code, raw)
	}
	routingAliasCheckVolcPOST(t, e, "ark-b")

	// a qualifier naming the second key: the POST must go out signed with
	// that key
	if err := provider.SetKeyRoutingAlias("ark", provider.KeyID("ark-b"), "work"); err != nil {
		t.Fatal(err)
	}
	code, obj, raw = post("ark/doubao-seedance-2.0-mini@work")
	if code != 200 || obj["status"] != "queued" {
		t.Errorf("qualified video on the bound key: %d %s; want 200 on the bound key, not a refusal", code, raw)
	}
	routingAliasCheckVolcPOST(t, e, "ark-b")

	// every key isolated: an ordinary reference consumes nothing
	code, _, raw = post("ark/doubao-seedance-2.0-mini")
	if code < 400 {
		t.Errorf("ordinary video with every key isolated: %d; want a refusal", code)
	}
	if asked, _ := e.take(); len(asked) != 0 {
		t.Errorf("ordinary video with every key isolated reached the vendor: %v", asked)
	}
}
