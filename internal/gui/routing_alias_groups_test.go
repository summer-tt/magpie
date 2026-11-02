package gui

import (
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// groupsSetup saves a key provider of three named keys and binds its second
// key to the routing alias "work" through the real UI API. It returns a
// reader of GET /api/groups through the real handler. The positive
// baseline — the alias POST saving — runs here, so a failure there means a
// broken fixture, not the behavior under test.
func groupsSetup(t *testing.T) func() *httptest.ResponseRecorder {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	p := provider.Provider{ID: "alias-pick", Name: "Alias pick", Chat: "http://127.0.0.1:1/v1",
		Key: "key-first", KeyName: "First",
		Keys:   []provider.KeyAccount{{Name: "Second", Key: "key-second"}, {Name: "Third", Key: "key-third"}},
		Models: []string{"m"}}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	api := Handler(nil, nil)
	b, _ := json.Marshal(map[string]string{"id": p.ID, "ref": provider.KeyID("key-second"), "alias": "work"})
	w := httptest.NewRecorder()
	api.ServeHTTP(w, httptest.NewRequest("POST", "/api/keys/alias", strings.NewReader(string(b))))
	if w.Code != 200 {
		t.Fatalf("baseline: setting the key's routing alias: %d %s", w.Code, w.Body.String())
	}
	return func() *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		api.ServeHTTP(w, httptest.NewRequest("GET", "/api/groups", nil))
		if w.Code != 200 {
			t.Fatalf("baseline: reading /api/groups: %d %s", w.Code, w.Body.String())
		}
		return w
	}
}

type groupsPickJSON struct {
	Models []struct {
		ID           string `json:"id"`
		RoutingAlias string `json:"routingAlias"`
		BaseID       string `json:"baseId"`
	} `json:"models"`
	Groups []struct {
		ID       string `json:"id"`
		Patterns []struct {
			Pattern string `json:"pattern"`
			Models  int    `json:"models"`
		} `json:"patterns"`
		MemberInfo []struct {
			ID    string `json:"id"`
			Ready bool   `json:"ready"`
			Model string `json:"model"`
			On    int    `json:"on"`
		} `json:"memberInfo"`
	} `json:"groups"`
	Pools []struct {
		Provider string   `json:"provider"`
		Kind     string   `json:"kind"`
		Who      []string `json:"who"`
	} `json:"pools"`
}

// The Routing page's member picker is fed by groups.models: the derived
// qualified entry must be there for a group to name it explicitly, marked
// with its routing alias and base model (design §9), and a pattern's count
// stays over base models only, so p/* never sweeps every account's variant
// into a group by itself.
func TestRoutingAliasGroupsOfferQualifiedPickerEntries(t *testing.T) {
	read := groupsSetup(t)
	if err := provider.SaveGroup(provider.Group{ID: "wide", Name: "Wide", Match: []string{"alias-pick/*"}}); err != nil {
		t.Fatal(err)
	}
	var out groupsPickJSON
	if err := json.Unmarshal(read().Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	base, derived := false, false
	for _, m := range out.Models {
		switch m.ID {
		case "alias-pick/m":
			base = true
			if m.RoutingAlias != "" {
				t.Errorf("base model carries a routing alias: %+v", m)
			}
		case "alias-pick/m@work":
			derived = true
			if m.RoutingAlias != "work" || m.BaseID != "alias-pick/m" {
				t.Errorf("qualified entry must say its routing alias and base model: %+v", m)
			}
		}
	}
	if !base {
		t.Error("the base model is missing from groups.models")
	}
	if !derived {
		t.Error("the qualified entry alias-pick/m@work is missing from groups.models, so no group can name it")
	}
	for _, g := range out.Groups {
		if g.ID != "wide" {
			continue
		}
		if len(g.Patterns) != 1 || g.Patterns[0].Pattern != "alias-pick/*" {
			t.Fatalf("the pattern group's hits: %+v", g.Patterns)
		}
		if g.Patterns[0].Models != 1 {
			t.Errorf("alias-pick/* matched %d models; a pattern counts base models, not each account's qualified variant", g.Patterns[0].Models)
		}
	}
}

// A provider's own pool on the Routing page says who its ordinary requests
// spread over: a key bound to a routing alias is not one of them, and a
// group's qualified member says the one owner it goes to (design §5, §7).
func TestRoutingAliasGroupsPoolAndQualifiedMember(t *testing.T) {
	read := groupsSetup(t)
	if err := provider.SaveGroup(provider.Group{ID: "shift", Name: "Shift", Members: []string{"alias-pick/m@work"}}); err != nil {
		t.Fatal(err)
	}
	var out groupsPickJSON
	if err := json.Unmarshal(read().Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	pool := -1
	for i, p := range out.Pools {
		if p.Provider == "alias-pick" {
			pool = i
		}
	}
	if pool < 0 {
		t.Fatal("the provider's pool is missing from /api/groups")
	}
	who := out.Pools[pool].Who
	for _, named := range who {
		if named == "Second" {
			t.Errorf("the pool says ordinary routing spreads over %v: the key with the routing alias must be left out", who)
		}
	}
	if len(who) == 0 {
		t.Error("the pool lists no ordinary key at all")
	}
	member := false
	for _, g := range out.Groups {
		if g.ID != "shift" {
			continue
		}
		for _, m := range g.MemberInfo {
			if m.ID != "alias-pick/m@work" {
				continue
			}
			member = true
			if !m.Ready || m.Model != "m" {
				t.Errorf("the qualified member must resolve ready to the plain model: %+v", m)
			}
			if m.On != 1 {
				t.Errorf("the qualified member says %d keys on; it goes to the one key its alias binds", m.On)
			}
		}
	}
	if !member {
		t.Error("the qualified member is missing from the group's memberInfo")
	}
}
