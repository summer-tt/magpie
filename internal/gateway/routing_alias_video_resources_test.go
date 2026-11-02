package gateway

import (
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/provider"
)

// A video is asked after with the key that started it, and a gateway key
// held to a qualified model may ask after its own video: the resource's
// authorization follows the creating key and the qualified grant it was
// started under, never a wider base. These tests reuse the custom videos
// fake (openAIVideos), the fake remote magpie and the fake Volcengine of
// the creation tests, all on loopback.

// pollsUntilDone polls a video until the vendor says it is completed, so
// the asks after it can be told by their key.
func pollsUntilDone(t *testing.T, ask func(method, path, body string) (int, map[string]any, []byte), id string) {
	t.Helper()
	for i := 0; i < 4; i++ {
		code, obj, raw := ask(http.MethodGet, "/v1/videos/"+id, "")
		if code != 200 {
			t.Fatalf("poll %d of %s: %d %s", i+1, id, code, raw)
		}
		if obj["status"] == "completed" {
			return
		}
	}
	t.Fatalf("video %s never completed", id)
}

// A gateway key whose models are exactly the qualified reference may
// start that video and ask after it: the start works (onKeys plans the
// bound key), but the poll and the bytes are refused, because videoMaker
// hands back the provider without its routing selection and
// videoModelHeld judges the base model alone. The grant widens to
// neither the ordinary base nor another owner, and accounts still take
// their intersection. After the alias retires the started video keeps
// its creating key, while the old reference starts nothing new.
func TestRoutingAliasCustomVideoQualifiedGrantResources(t *testing.T) {
	fresh(t)
	up := newOpenAIVideos(t)
	saveFetched(t, provider.Provider{ID: "studio", Name: "Studio", Chat: up.URL + "/v1", Key: "kA", Keys: []provider.KeyAccount{{Key: "kB"}}})
	if err := provider.SetKeyRoutingAlias("studio", provider.KeyID("kA"), "work"); err != nil {
		t.Fatal(err)
	}
	if err := provider.SetKeyRoutingAlias("studio", provider.KeyID("kB"), "team"); err != nil {
		t.Fatal(err)
	}
	h := New().Handler()
	as := func(secret, method, path, body string) (int, map[string]any, []byte) {
		return asKey(t, h, secret, method, path, body)
	}

	// positive baseline: an unrestricted caller starts, polls and fetches
	// the qualified video, every ask on the bound first key
	code, obj, raw := as("", "POST", "/v1/videos", `{"model":"studio/vid-1@work","prompt":"a kite"}`)
	id, _ := obj["id"].(string)
	if code != 200 || id == "" {
		t.Fatalf("baseline qualified start: %d %s", code, raw)
	}
	asked := up.log()
	if len(asked) == 0 || !strings.HasSuffix(asked[len(asked)-1], " kA") {
		t.Fatalf("baseline start asked %v; want the bound first key", asked)
	}
	pollsUntilDone(t, func(method, path, body string) (int, map[string]any, []byte) { return as("", method, path, body) }, id)
	code, _, raw = as("", "GET", "/v1/videos/"+id+"/content", "")
	if code != 200 || !strings.HasSuffix(string(raw), "fake-mp4") {
		t.Fatalf("baseline content: %d %s", code, raw)
	}
	// the videos API's asks alone: a key's model-list GET is discovery,
	// not a video's consumption
	if got := routingAliasVideoAsks(up.log()); len(got) == 0 {
		t.Fatal("baseline made no video asks")
	} else {
		for _, a := range got {
			if !strings.HasSuffix(a, " kA") {
				t.Fatalf("video asks %v; every one belongs to the bound first key, model discovery apart", up.log())
			}
		}
	}

	// a gateway key held to exactly the qualified reference starts the
	// video on the same bound key
	keys, secrets := newCaller(t, "ExactWork")
	if _, err := access.Update("models-key", access.Change{Key: keys[0].ID, Models: []string{"studio/vid-1@work"}}); err != nil {
		t.Fatal(err)
	}
	code, obj, raw = as(secrets[0], "POST", "/v1/videos", `{"model":"studio/vid-1@work","prompt":"a kite"}`)
	qid, _ := obj["id"].(string)
	if code != 200 || qid == "" {
		t.Fatalf("qualified-grant start: %d %s; the resource assertions below need this start to work", code, raw)
	}
	// the poll and the bytes must be its: the creating key and the base
	// under the same qualified grant
	before := len(up.log())
	code, obj, raw = as(secrets[0], "GET", "/v1/videos/"+qid, "")
	if code != 200 || obj["status"] != "completed" {
		t.Errorf("qualified-grant poll: %d %s; a key granted exactly the qualified model may ask after the video it started", code, raw)
	}
	code, _, raw = as(secrets[0], "GET", "/v1/videos/"+qid+"/content", "")
	if code != 200 || !strings.HasSuffix(string(raw), "fake-mp4") {
		t.Errorf("qualified-grant content: %d %s; the grant covers the video it started, on its creating key", code, raw)
	}
	for _, a := range routingAliasVideoAsks(up.log()[before:]) { // the video asks the poll and content made, if any
		if !strings.HasSuffix(a, " kA") {
			t.Errorf("qualified-grant video asks %v; the video belongs to its creating key", routingAliasVideoAsks(up.log()[before:]))
		}
	}

	// the same caller with its accounts held to the creating key alone:
	// the restriction takes its intersection, never the resource away
	if _, err := access.Update("accounts-key", access.Change{Key: keys[0].ID, Accounts: []string{"studio/" + provider.KeyID("kA")}}); err != nil {
		t.Fatal(err)
	}
	before = len(up.log())
	code, obj, raw = as(secrets[0], "GET", "/v1/videos/"+qid, "")
	if code != 200 || obj["status"] != "completed" {
		t.Errorf("qualified-grant poll with accounts held to the creating key: %d %s; the accounts intersection keeps the video reachable on its own key", code, raw)
	}
	code, _, raw = as(secrets[0], "GET", "/v1/videos/"+qid+"/content", "")
	if code != 200 || !strings.HasSuffix(string(raw), "fake-mp4") {
		t.Errorf("qualified-grant content with accounts held to the creating key: %d %s", code, raw)
	}
	for _, a := range routingAliasVideoAsks(up.log()[before:]) {
		if !strings.HasSuffix(a, " kA") {
			t.Errorf("qualified-grant video asks with accounts held = %v; the video belongs to its creating key", routingAliasVideoAsks(up.log()[before:]))
		}
	}

	// the grant widens to neither the ordinary base nor another owner
	for _, model := range []string{"studio/vid-1", "studio/vid-1@team"} {
		before = len(up.log())
		code, obj, raw = as(secrets[0], "POST", "/v1/videos", `{"model":"`+model+`","prompt":"no"}`)
		if code != 403 || !strings.Contains(errorOf(obj), "may not use") {
			t.Errorf("%s with a qualified-only grant: %d %s; want 403", model, code, raw)
		}
		if got := routingAliasVideoAsks(up.log()[before:]); len(got) != 0 {
			t.Errorf("%s with a qualified-only grant reached the videos API: %v", model, got)
		}
	}

	// accounts still take their intersection with the grant
	ikeys, isecrets := newCaller(t, "Intersect")
	if _, err := access.Update("models-key", access.Change{Key: ikeys[0].ID, Models: []string{"studio/vid-1@work"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := access.Update("accounts-key", access.Change{Key: ikeys[0].ID, Accounts: []string{"studio/" + provider.KeyID("kB")}}); err != nil {
		t.Fatal(err)
	}
	before = len(up.log())
	code, obj, raw = as(isecrets[0], "POST", "/v1/videos", `{"model":"studio/vid-1@work","prompt":"no"}`)
	if code != 403 || !strings.Contains(errorOf(obj), "not allowed to use the accounts") {
		t.Errorf("qualified grant beside a foreign account: %d %s; want the accounts intersection to refuse", code, raw)
	}
	if got := routingAliasVideoAsks(up.log()[before:]); len(got) != 0 {
		t.Errorf("qualified grant beside a foreign account reached the videos API: %v", got)
	}
	// the same intersection refuses the resource: a caller held to the
	// other key neither polls nor fetches a video it did not start
	for _, path := range []string{"/v1/videos/" + qid, "/v1/videos/" + qid + "/content"} {
		before = len(up.log())
		code, obj, raw = as(isecrets[0], "GET", path, "")
		if code != 403 || !strings.Contains(errorOf(obj), "not allowed to use the accounts") {
			t.Errorf("held to the other key, %s: %d %s; want the accounts intersection to refuse the resource", path, code, raw)
		}
		if got := routingAliasVideoAsks(up.log()[before:]); len(got) != 0 {
			t.Errorf("held to the other key, %s reached the videos API: %v", path, got)
		}
	}

	// the alias retiring changes neither the started video's key nor its
	// availability; the old reference starts nothing new
	if err := provider.SetKeyRoutingAlias("studio", provider.KeyID("kA"), ""); err != nil {
		t.Fatal(err)
	}
	before = len(up.log())
	code, obj, raw = as("", "GET", "/v1/videos/"+id, "")
	if code != 200 || obj["status"] != "completed" {
		t.Errorf("poll after the alias retired: %d %s; a started video keeps its creating key", code, raw)
	}
	code, _, raw = as("", "GET", "/v1/videos/"+id+"/content", "")
	if code != 200 || !strings.HasSuffix(string(raw), "fake-mp4") {
		t.Errorf("content after the alias retired: %d %s", code, raw)
	}
	if got := routingAliasVideoAsks(up.log()[before:]); len(got) != 2 || !strings.HasSuffix(got[0], " kA") || !strings.HasSuffix(got[1], " kA") {
		t.Errorf("video asks after the alias retired = %v; want the poll and the content alone, on the video's own creating key", got)
	}
	// the granted key asks after its own started video still: the
	// authorization is the creating key and its grant, not an alias that
	// is still active
	before = len(up.log())
	code, obj, raw = as(secrets[0], "GET", "/v1/videos/"+qid, "")
	if code != 200 || obj["status"] != "completed" {
		t.Errorf("granted key's poll after the alias retired: %d %s; a started video's authorization is historical, not active-only", code, raw)
	}
	code, _, raw = as(secrets[0], "GET", "/v1/videos/"+qid+"/content", "")
	if code != 200 || !strings.HasSuffix(string(raw), "fake-mp4") {
		t.Errorf("granted key's content after the alias retired: %d %s", code, raw)
	}
	for _, a := range routingAliasVideoAsks(up.log()[before:]) {
		if !strings.HasSuffix(a, " kA") {
			t.Errorf("granted key's video asks after the alias retired = %v; the video's own creating key alone", routingAliasVideoAsks(up.log()[before:]))
		}
	}
	before = len(up.log())
	code, obj, raw = as("", "POST", "/v1/videos", `{"model":"studio/vid-1@work","prompt":"no"}`)
	if code != 404 || !strings.Contains(strings.ToLower(errorOf(obj)), "alias") {
		t.Errorf("start over the retired reference: %d %s; want 404 naming the routing alias as the reason", code, raw)
	}
	if got := routingAliasVideoAsks(up.log()[before:]); len(got) != 0 {
		t.Errorf("start over the retired reference reached the videos API: %v", got)
	}
}

// A video of another magpie started on a bound second key is asked after
// on that key, never on the provider's default first key. On the frozen
// candidate the qualified start itself is refused, and the test says so
// there; the resource assertions stand for the fixed start.
func TestRoutingAliasRemoteMagpieVideoResourcesKeepCreatingKey(t *testing.T) {
	e := newRoutingAliasRemoteMagpie(t)
	routingAliasSaveRemote(t, e)
	h := New().Handler()
	as := func(method, path, body string) (int, map[string]any, []byte) {
		return asKey(t, h, "", method, path, body)
	}

	// the flow as it is: a video started, polled and fetched on the
	// first key, its asks all signed with that key
	code, obj, raw := as("POST", "/v1/videos", `{"model":"office/grok/grok-imagine-video","prompt":"a kite"}`)
	id, _ := obj["id"].(string)
	if code != 200 || id == "" {
		t.Fatalf("baseline start: %d %s", code, raw)
	}
	routingAliasCheckPOST(t, e, "rk-1", "grok/grok-imagine-video")
	pollsUntilDone(t, as, id)
	code, _, raw = as("GET", "/v1/videos/"+id+"/content", "")
	if code != 200 || string(raw) != "fake-mp4-bytes" {
		t.Fatalf("baseline content: %d %s", code, raw)
	}
	if asked, _ := e.take(); len(routingAliasVideoAsks(asked)) == 0 {
		t.Fatal("baseline polls left no asks at the remote's videos API")
	}

	// a qualifier naming the second key: the video is started there, and
	// asked after there — the poll and the content never fall back to the
	// provider's default key
	if err := provider.SetKeyRoutingAlias("office", provider.KeyID("rk-2"), "work"); err != nil {
		t.Fatal(err)
	}
	code, obj, raw = as("POST", "/v1/videos", `{"model":"office/grok/grok-imagine-video@work","prompt":"a kite"}`)
	qid, _ := obj["id"].(string)
	if code != 200 || qid == "" {
		t.Fatalf("qualified start on the bound key: %d %s; the resource assertions below need this start to work", code, raw)
	}
	routingAliasCheckPOST(t, e, "rk-2", "grok/grok-imagine-video")
	pollsUntilDone(t, as, qid)
	code, _, raw = as("GET", "/v1/videos/"+qid+"/content", "")
	if code != 200 || string(raw) != "fake-mp4-bytes" {
		t.Errorf("qualified content: %d %s", code, raw)
	}
	asked, _ := e.take()
	video := routingAliasVideoAsks(asked)
	if len(video) == 0 {
		t.Fatal("the qualified video was never asked after at the remote's videos API")
	}
	for _, a := range video {
		if !strings.HasSuffix(a, " rk-2") {
			t.Errorf("ask after the qualified video = %q; the video belongs to the key that started it, not the default one", a)
		}
	}

	// the resource's accounts intersection: a caller held to the creating
	// key alone asks after the video on it; one held to the first key
	// neither polls nor fetches it. Both carry the base model grant, so a
	// refusal is the accounts intersection's, not the models'. These stand
	// once the qualified start above works on the fixed candidate.
	bkeys, bsecrets := newCaller(t, "OnCreatingKey", "OnFirstKey")
	for i, acct := range []string{"rk-2", "rk-1"} {
		if _, err := access.Update("models-key", access.Change{Key: bkeys[i].ID, Models: []string{"office/grok/grok-imagine-video"}}); err != nil {
			t.Fatal(err)
		}
		if _, err := access.Update("accounts-key", access.Change{Key: bkeys[i].ID, Accounts: []string{"office/" + provider.KeyID(acct)}}); err != nil {
			t.Fatal(err)
		}
	}
	e.take()
	code, obj, raw = asKey(t, h, bsecrets[0], "GET", "/v1/videos/"+qid, "")
	if code != 200 || obj["status"] != "completed" {
		t.Errorf("creating-key caller's poll: %d %s; the video is its key's to ask after", code, raw)
	}
	code, _, raw = asKey(t, h, bsecrets[0], "GET", "/v1/videos/"+qid+"/content", "")
	if code != 200 || string(raw) != "fake-mp4-bytes" {
		t.Errorf("creating-key caller's content: %d %s", code, raw)
	}
	asked, _ = e.take()
	for _, a := range routingAliasVideoAsks(asked) {
		if !strings.HasSuffix(a, " rk-2") {
			t.Errorf("creating-key caller's asks = %q; the video belongs to the key that started it", a)
		}
	}
	e.take()
	for _, path := range []string{"/v1/videos/" + qid, "/v1/videos/" + qid + "/content"} {
		code, obj, raw = asKey(t, h, bsecrets[1], "GET", path, "")
		if code != 403 || !strings.Contains(errorOf(obj), "not allowed to use the accounts") {
			t.Errorf("first-key caller, %s: %d %s; want the accounts intersection to refuse the resource", path, code, raw)
		}
	}
	if asked, _ := e.take(); len(routingAliasVideoAsks(asked)) != 0 {
		t.Errorf("first-key caller reached the remote's videos API: %v", routingAliasVideoAsks(asked))
	}
}

// The same resource rule at a single-plan Volcengine: a video started on
// a bound second key is polled and fetched over the tasks API on that
// key, never on the default first one. On the frozen candidate the
// qualified start itself is refused, and the test says so there.
func TestRoutingAliasVolcengineVideoResourcesKeepCreatingKey(t *testing.T) {
	e := newRoutingAliasVolcVideo(t)
	h := New().Handler()
	as := func(method, path, body string) (int, map[string]any, []byte) {
		return asKey(t, h, "", method, path, body)
	}

	// the flow as it is, on the first key
	code, obj, raw := as("POST", "/v1/videos", `{"model":"ark/doubao-seedance-2.0-mini","prompt":"a magpie","seconds":5,"size":"854x480"}`)
	id, _ := obj["id"].(string)
	if code != 200 || id == "" {
		t.Fatalf("baseline start: %d %s", code, raw)
	}
	routingAliasCheckVolcPOST(t, e, "ark-a")
	pollsUntilDone(t, as, id)
	code, _, raw = as("GET", "/v1/videos/"+id+"/content", "")
	if code != 200 || string(raw) != "ark-video-bytes" {
		t.Fatalf("baseline content: %d %s", code, raw)
	}
	if asked, _ := e.take(); !slices.ContainsFunc(asked, func(a string) bool { return strings.Contains(a, "/contents/generations/tasks") }) {
		t.Fatal("baseline polls left no asks at the tasks API")
	}

	// started on the bound second key: every ask over the tasks API is
	// signed with it
	if err := provider.SetKeyRoutingAlias("ark", provider.KeyID("ark-b"), "work"); err != nil {
		t.Fatal(err)
	}
	code, obj, raw = as("POST", "/v1/videos", `{"model":"ark/doubao-seedance-2.0-mini@work","prompt":"a magpie","seconds":5,"size":"854x480"}`)
	qid, _ := obj["id"].(string)
	if code != 200 || qid == "" {
		t.Fatalf("qualified start on the bound key: %d %s; the resource assertions below need this start to work", code, raw)
	}
	routingAliasCheckVolcPOST(t, e, "ark-b")
	pollsUntilDone(t, as, qid)
	code, _, raw = as("GET", "/v1/videos/"+qid+"/content", "")
	if code != 200 || string(raw) != "ark-video-bytes" {
		t.Errorf("qualified content: %d %s", code, raw)
	}
	asked, _ := e.take()
	var api []string
	for _, a := range asked {
		if strings.Contains(a, "/contents/generations/tasks") {
			api = append(api, a)
		}
	}
	if len(api) == 0 {
		t.Fatal("the qualified video was never asked after over the tasks API")
	}
	for _, a := range api {
		if !strings.HasSuffix(a, " ark-b") {
			t.Errorf("ask after the qualified video = %q; the video belongs to the key that started it, not the default one", a)
		}
	}

	// the resource's accounts intersection, as the remote's: a caller held
	// to the creating key alone asks after the video over the tasks API on
	// it; one held to the first key is refused with nothing asked. Both
	// carry the base model grant, so a refusal is the accounts one. These
	// stand once the qualified start above works on the fixed candidate.
	bkeys, bsecrets := newCaller(t, "OnCreatingKey", "OnFirstKey")
	for i, acct := range []string{"ark-b", "ark-a"} {
		if _, err := access.Update("models-key", access.Change{Key: bkeys[i].ID, Models: []string{"ark/doubao-seedance-2.0-mini"}}); err != nil {
			t.Fatal(err)
		}
		if _, err := access.Update("accounts-key", access.Change{Key: bkeys[i].ID, Accounts: []string{"ark/" + provider.KeyID(acct)}}); err != nil {
			t.Fatal(err)
		}
	}
	e.take()
	code, obj, raw = asKey(t, h, bsecrets[0], "GET", "/v1/videos/"+qid, "")
	if code != 200 || obj["status"] != "completed" {
		t.Errorf("creating-key caller's poll: %d %s; the video is its key's to ask after", code, raw)
	}
	code, _, raw = asKey(t, h, bsecrets[0], "GET", "/v1/videos/"+qid+"/content", "")
	if code != 200 || string(raw) != "ark-video-bytes" {
		t.Errorf("creating-key caller's content: %d %s", code, raw)
	}
	asked, _ = e.take()
	for _, a := range asked {
		if strings.Contains(a, "/contents/generations/tasks") && !strings.HasSuffix(a, " ark-b") {
			t.Errorf("creating-key caller's tasks ask = %q; the video belongs to the key that started it", a)
		}
	}
	e.take()
	for _, path := range []string{"/v1/videos/" + qid, "/v1/videos/" + qid + "/content"} {
		code, obj, raw = asKey(t, h, bsecrets[1], "GET", path, "")
		if code != 403 || !strings.Contains(errorOf(obj), "not allowed to use the accounts") {
			t.Errorf("first-key caller, %s: %d %s; want the accounts intersection to refuse the resource", path, code, raw)
		}
	}
	if asked, _ := e.take(); slices.ContainsFunc(asked, func(a string) bool { return strings.Contains(a, "/contents/generations/tasks") }) {
		t.Errorf("first-key caller reached the tasks API: %v", asked)
	}
}
