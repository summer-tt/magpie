package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

// A routing group's row on GET /v1/models says, to every client and not
// to another magpie alone, the APIs a request goes on as it is to
// whichever member answers it (native_endpoints: the APIs every member
// takes, a group in the group opened to its models), and the API the list
// recommends (magpie_prefers): Responses where the members share it, or
// where an agent is wired there though they share none. A group whose
// members share no API says neither, and one kept on Chat with Chat or
// Messages alone shared recommends nothing: another magpie reading a Chat
// here turned a Messages request into Chat before it passed the group on.
// The drawers (X-Magpie-Drawers) are not asked for, and none is listed.
func TestModelsListGroupNativeEndpoints(t *testing.T) {
	fresh(t)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":"x","object":"response","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	t.Cleanup(up.Close)
	// both and twin speak Chat first and Responses too, so their GPT is
	// best on Chat there; resp speaks Responses alone
	both := provider.Provider{ID: "both", Name: "Both", Key: "k", Chat: up.URL + "/v1", Responses: up.URL + "/v1", Models: []string{"gpt-5.5"}}
	twin := provider.Provider{ID: "twin", Name: "Twin", Key: "k", Chat: up.URL + "/v1", Responses: up.URL + "/v1", Models: []string{"gpt-5.5"}}
	resp := provider.Provider{ID: "resp", Name: "Resp", Key: "k", Responses: up.URL + "/v1", Models: []string{"gpt-5.5"}}
	chat := provider.Provider{ID: "chatp", Name: "Chat", Key: "k", Chat: up.URL + "/v1", Models: []string{"gpt-5.5"}}
	anth := provider.Provider{ID: "anth", Name: "Anth", Key: "k", Anthropic: up.URL, Models: []string{"claude-sonnet-5"}}
	for _, p := range []provider.Provider{both, twin, resp, chat, anth} {
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}
	if n := both.Native("gpt-5.5"); n != provider.Chat {
		t.Fatalf("the Chat-first relay's GPT is best on %q; the groups below assume Chat", n)
	}
	for _, g := range []provider.Group{
		// two Chat-first relays that serve Responses too: Responses is
		// shared, though GroupAPI keeps the group on Chat
		{ID: "dual", Name: "Dual", Members: []string{"both/gpt-5.5", "twin/gpt-5.5"}, Routing: provider.Ordered},
		// one best on Responses, one serving it too: wired there
		{ID: "respy", Name: "Respy", Members: []string{"resp/gpt-5.5", "both/gpt-5.5"}, Routing: provider.Ordered},
		// Responses alone and Chat alone: nothing shared, kept on Chat
		{ID: "split", Name: "Split", Members: []string{"resp/gpt-5.5", "chatp/gpt-5.5"}, Routing: provider.Ordered},
		// Messages alone: shared, but never recommended
		{ID: msgsOnly, Name: "Msgs", Members: []string{"anth/claude-sonnet-5"}, Routing: provider.Ordered},
		// a group in the group: its models are the outer group's members
		{ID: "inner", Name: "Inner", Members: []string{"resp/gpt-5.5"}, Routing: provider.Ordered},
		{ID: "nested", Name: "Nested", Members: []string{"group/inner", "both/gpt-5.5"}, Routing: provider.Ordered},
	} {
		if err := provider.SaveGroup(g); err != nil {
			t.Fatal(err)
		}
	}
	const chatPath, respPath, msgPath = "/v1/chat/completions", "/v1/responses", "/v1/messages"
	want := map[string]struct {
		native  []string
		prefers string
	}{
		"group/dual":        {[]string{chatPath, respPath}, respPath},
		"group/respy":       {[]string{respPath}, respPath},
		"group/split":       {nil, ""},
		"group/" + msgsOnly: {[]string{msgPath}, ""},
		// inner's Responses-only member and both's: Responses shared
		"group/nested": {[]string{respPath}, respPath},
	}
	for _, c := range []struct {
		name, header string
	}{
		{"a plain client", ""},
		{"another magpie", provider.DrawersHeader},
	} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/v1/models", nil)
		if c.header != "" {
			req.Header.Set(c.header, "1")
		}
		New().Handler().ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("%s: %d %s", c.name, rec.Code, rec.Body)
		}
		rows := listedRows(t, rec.Body.String())
		for id, w := range want {
			row, ok := rows[id]
			if !ok {
				t.Errorf("%s: %s isn't listed; have %v", c.name, id, keysOf(rows))
				continue
			}
			if got, _ := row["native_endpoints"].([]any); !equalPaths(got, w.native) {
				t.Errorf("%s: %s native_endpoints %v, want %v", c.name, id, got, w.native)
			}
			if got, _ := row["magpie_prefers"].(string); got != w.prefers {
				t.Errorf("%s: %s magpie_prefers %q, want %q", c.name, id, got, w.prefers)
			}
		}
		// without the drawers asked for, no image model is listed
		if c.header == "" {
			for id, row := range rows {
				if k, _ := row["kind"].(string); k == "image" {
					t.Errorf("%s: %s of kind image listed without the drawers", c.name, id)
				}
			}
		}
	}
}

// A model that is no group keeps its row as it was: native_endpoints is
// what its provider serves it on, and magpie_prefers its provider's own
// best API, told to a plain client now too and not to another magpie
// alone, saying nothing of the provider or an account on it.
func TestModelsListModelPrefersToldToEveryClient(t *testing.T) {
	fresh(t)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(404) }))
	t.Cleanup(up.Close)
	// Chat first and Responses too: best on Chat
	both := provider.Provider{ID: "both", Name: "Both", Key: "k", Chat: up.URL + "/v1", Responses: up.URL + "/v1", Models: []string{"gpt-5.5"}}
	if err := provider.Save(both); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name, header string
	}{
		{"a plain client", ""},
		{"another magpie", provider.DrawersHeader},
	} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/v1/models", nil)
		if c.header != "" {
			req.Header.Set(c.header, "1")
		}
		New().Handler().ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("%s: %d %s", c.name, rec.Code, rec.Body)
		}
		rows := listedRows(t, rec.Body.String())
		row, ok := rows["both/gpt-5.5"]
		if !ok {
			t.Fatalf("%s: both/gpt-5.5 isn't listed; have %v", c.name, keysOf(rows))
		}
		if got, _ := row["native_endpoints"].([]any); !equalPaths(got, []string{"/v1/chat/completions", "/v1/responses"}) {
			t.Errorf("%s: native_endpoints %v", c.name, got)
		}
		if got, _ := row["magpie_prefers"].(string); got != "/v1/chat/completions" {
			t.Errorf("%s: magpie_prefers %q, want the provider's own best, Chat", c.name, got)
		}
	}
}

// GET /v1/models/{id} says of a routing group what the list does, so a
// client that reads one model isn't told less than one that reads them
// all.
func TestModelGroupNativeEndpointsLikeTheList(t *testing.T) {
	fresh(t)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(404) }))
	t.Cleanup(up.Close)
	for _, p := range []provider.Provider{
		{ID: "resp", Name: "Resp", Key: "k", Responses: up.URL + "/v1", Models: []string{"gpt-5.5"}},
		{ID: "chatp", Name: "Chat", Key: "k", Chat: up.URL + "/v1", Models: []string{"gpt-5.5"}},
	} {
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}
	for _, g := range []provider.Group{
		{ID: "respy", Name: "Respy", Members: []string{"resp/gpt-5.5"}, Routing: provider.Ordered},
		{ID: "split", Name: "Split", Members: []string{"resp/gpt-5.5", "chatp/gpt-5.5"}, Routing: provider.Ordered},
	} {
		if err := provider.SaveGroup(g); err != nil {
			t.Fatal(err)
		}
	}
	for id, want := range map[string][]string{
		"group/respy": {"/v1/responses"},
		"group/split": nil,
	} {
		list, one := httptest.NewRecorder(), httptest.NewRecorder()
		h := New().Handler()
		h.ServeHTTP(list, httptest.NewRequest("GET", "/v1/models", nil))
		h.ServeHTTP(one, httptest.NewRequest("GET", "/v1/models/"+id, nil))
		if one.Code != 200 || list.Code != 200 {
			t.Fatalf("%s: list %d, one %d %s", id, list.Code, one.Code, one.Body)
		}
		var row map[string]any
		if err := json.Unmarshal(one.Body.Bytes(), &row); err != nil {
			t.Fatal(err)
		}
		got, _ := row["native_endpoints"].([]any)
		if !equalPaths(got, want) {
			t.Errorf("%s: GET /v1/models/{id} native_endpoints %v, want %v", id, got, want)
		}
		if p, _ := row["magpie_prefers"].(string); (p == "/v1/responses") != (id == "group/respy") {
			t.Errorf("%s: GET /v1/models/{id} magpie_prefers %q", id, p)
		}
		inList := listedRows(t, list.Body.String())[id]
		lg, _ := inList["native_endpoints"].([]any)
		if !slices.Equal(lg, got) {
			t.Errorf("%s: the list says %v and the one model %v", id, lg, got)
		}
	}
}

// groupTold, on its own: the members are looked up once and the shared
// APIs once; a group whose members share none is recommended Responses
// only where GroupAPI wires an agent there (a member best on Responses,
// every other translating anyway), never Chat; a group the finder doesn't
// know, and a model, is left as it is.
func TestGroupTold(t *testing.T) {
	resp := provider.Provider{ID: "resp", Responses: "http://127.0.0.1:1/v1"}
	both := provider.Provider{ID: "both", Chat: "http://127.0.0.1:1/v1", Responses: "http://127.0.0.1:1/v1"}
	chat := provider.Provider{ID: "chatp", Chat: "http://127.0.0.1:1/v1"}
	anth := provider.Provider{ID: "anth", Anthropic: "http://127.0.0.1:1"}
	// a Claude Code subscription: every request to it is translated anyway
	claude := provider.Provider{ID: "claude", Anthropic: "http://127.0.0.1:1", Account: &provider.Account{Agent: "claude"}}
	of := func(ps ...provider.Provider) []provider.Member {
		var ms []provider.Member
		for _, p := range ps {
			ms = append(ms, provider.Member{Provider: p, Model: "gpt-5.5"})
		}
		return ms
	}
	group := func(id string) provider.Entry { return provider.Entry{ID: "group/" + id, Group: "x"} }
	for _, c := range []struct {
		name    string
		e       provider.Entry
		ms      []provider.Member
		found   bool
		native  []string
		prefers string
	}{
		{"wired there, members share none", group("a"), of(resp, claude), true, nil, "/v1/responses"},
		{"shared Responses, kept on Chat", group("b"), of(both, both), true, []string{"/v1/chat/completions", "/v1/responses"}, "/v1/responses"},
		{"shared Chat alone", group("c"), of(both, chat), true, []string{"/v1/chat/completions"}, ""},
		{"shared Messages alone", group("d"), of(anth, anth), true, []string{"/v1/messages"}, ""},
		{"the finder doesn't know it", group("e"), nil, false, nil, ""},
	} {
		looks := 0
		find := func(id string) (provider.Group, []provider.Member, bool) {
			looks++
			return provider.Group{}, c.ms, c.found
		}
		m := map[string]any{"id": c.e.ID}
		groupTold(m, c.e, find)
		if looks != 1 {
			t.Errorf("%s: the finder asked %d times, want once", c.name, looks)
		}
		if got, _ := m["native_endpoints"].([]string); !slices.Equal(got, c.native) {
			t.Errorf("%s: native_endpoints %v, want %v", c.name, got, c.native)
		}
		if got, _ := m["magpie_prefers"].(string); got != c.prefers {
			t.Errorf("%s: magpie_prefers %q, want %q", c.name, got, c.prefers)
		}
	}
	// a model is no group's row: nothing of a group is added
	m := map[string]any{"id": "resp/gpt-5.5"}
	groupTold(m, provider.Entry{ID: "resp/gpt-5.5"}, func(string) (provider.Group, []provider.Member, bool) {
		t.Fatal("a model's row looked its group up")
		return provider.Group{}, nil, false
	})
	if len(m) != 1 {
		t.Errorf("a model's row had a group's fields added: %v", m)
	}
}

const msgsOnly = "msgs-only"

// listedRows are the chat rows of a /v1/models answer by id.
func listedRows(t *testing.T, body string) map[string]map[string]any {
	t.Helper()
	var out struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatal(err)
	}
	rows := map[string]map[string]any{}
	for _, m := range out.Data {
		if k, _ := m["kind"].(string); k == "" {
			rows[m["id"].(string)] = m
		}
	}
	return rows
}

func keysOf(m map[string]map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// equalPaths says whether got, as JSON decoded it, is want: nil and
// empty are one, as a field left out is.
func equalPaths(got []any, want []string) bool {
	if len(want) == 0 {
		return len(got) == 0
	}
	if len(got) != len(want) {
		return false
	}
	for i, p := range got {
		if s, _ := p.(string); s != want[i] {
			return false
		}
	}
	return true
}

// A group magpie found (auto/<model>) is a routing group like one of the
// user's own: its row says the APIs its members share and recommends
// Responses, and is marked magpie_group, so another magpie that has this
// one as its provider keeps a request for it going on in the client's
// API rather than translating it to the one the members share before it
// is passed on. The remote's own models are unchanged: one served on
// Messages alone is asked there, as before.
func TestRemoteMagpieAutoGroupPassesOnInTheClientsAPI(t *testing.T) {
	fresh(t)
	var mu sync.Mutex
	var hops []string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body)
		mu.Lock()
		hops = append(hops, r.URL.Path)
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(
			`data: {"type":"response.created","response":{"id":"r1","model":"m2"}}`,
			`data: {"type":"response.output_text.delta","delta":"hello"}`,
			`data: {"type":"response.completed","response":{"id":"r1","usage":{"input_tokens":1,"output_tokens":1}}}`))
	}))
	t.Cleanup(up.Close)
	// m2 is listed by two providers, so magpie finds auto/m2, whose
	// members both relay Responses; m1 by one, served on Messages alone
	for _, p := range []provider.Provider{
		{ID: "ra", Name: "RA", Key: "k", Responses: up.URL + "/v1", Models: []string{"m1", "m2"}, Anthropic: up.URL},
		{ID: "rb", Name: "RB", Key: "k", Responses: up.URL + "/v1", Models: []string{"m2"}},
	} {
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}
	h := New().Handler()
	remote := httptest.NewServer(h)
	t.Cleanup(remote.Close)
	id, err := provider.Add(provider.Provider{ID: "office", Name: "Office", Preset: provider.RemoteMagpiePreset, Chat: strings.TrimPrefix(remote.URL, "http://")})
	if err != nil {
		t.Fatal(err)
	}
	office, err := provider.Find(id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := office.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	live, _, _ := catalog.Live("office")
	apis := map[string][]string{}
	for _, m := range live {
		apis[m.ID] = m.APIs
	}
	// the found group is served on no one API here, not the Responses
	// both its members relay; its models keep the APIs their list gives
	if a, ok := apis["auto/m2"]; !ok || a != nil {
		t.Errorf("the remote's found group is served on no one API here; its APIs are %v (%v)", a, ok)
	}
	if a := apis["rb/m2"]; !slices.Equal(a, []string{"responses"}) {
		t.Errorf("the remote's own m2 is served on %v there; want Responses, as its list says", a)
	}
	// the list this magpie gives marks the group and recommends Responses
	req, _ := http.NewRequest("GET", remote.URL+"/v1/models", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	rows := listedRows(t, string(b))
	if row := rows["auto/m2"]; row == nil || row["magpie_group"] != true || row["magpie_prefers"] != "/v1/responses" {
		t.Errorf("auto/m2's row %v, want marked magpie_group and preferring Responses; have %v", row, keysOf(rows))
	}
	if row := rows["ra/m1"]; row == nil || row["magpie_group"] != nil {
		t.Errorf("a model's row is marked a group: %v", row)
	}
	// a Responses request for the remote's found group goes on as it is
	mu.Lock()
	hops = nil
	mu.Unlock()
	code, body := post(t, "/v1/responses", `{"model":"office/auto/m2","input":"hi","stream":true}`)
	mu.Lock()
	got := slices.Clone(hops)
	mu.Unlock()
	if code != 200 || !strings.Contains(body, "hello") {
		t.Fatalf("%d %s", code, body)
	}
	if !slices.Equal(got, []string{"/v1/responses"}) {
		t.Errorf("the remote was asked on %v; want the request passed on in the client's API, /v1/responses", got)
	}
}
