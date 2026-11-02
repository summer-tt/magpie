package gateway

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// A Kimi Code plan's search service is asked with the provider's first
// key (kimiSearch writes p.Key straight into Authorization). magpie's own
// ordinary search — Settings naming the plan as the searcher, the chain
// chosenSearcher → modelSearch → searchBy → kimiSearch — must respect
// account isolation like every other magpie-initiated request: a key
// bound to a routing alias is not an ordinary key, and when every key is
// bound the service is not asked at all. These tests run against a fake
// plan served on loopback only; no real api.kimi.com address and no real
// credential is involved.

// routingAliasKimiPlan is a fake Kimi Code plan: its search service
// answers as kimi-cli reads it, recording the bearer key each search
// came with.
type routingAliasKimiPlan struct {
	*httptest.Server
	mu      sync.Mutex
	bearers []string
}

func newRoutingAliasKimiPlan(t *testing.T) *routingAliasKimiPlan {
	k := &routingAliasKimiPlan{}
	k.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/coding/v1/search" {
			http.NotFound(w, r)
			return
		}
		bearer := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		k.mu.Lock()
		k.bearers = append(k.bearers, bearer)
		k.mu.Unlock()
		io.WriteString(w, `{"search_results":[{"site_name":"Go","title":"Go downloads","url":"https://go.dev/dl/","snippet":"go1.27.1 is the latest release.","content":"","date":"2026-09-30"}]}`)
	}))
	t.Cleanup(k.Close)
	return k
}

func (k *routingAliasKimiPlan) take() []string {
	k.mu.Lock()
	defer k.mu.Unlock()
	out := slices.Clone(k.bearers)
	k.bearers = nil
	return out
}

// routingAliasKimiNamed saves a two-key Kimi Code plan on the fake plan
// and names it as Settings' searcher, the exact trigger the review
// describes: an ordinary web search made while the plan's first key is
// alias-isolated.
func routingAliasKimiNamed(t *testing.T, k *routingAliasKimiPlan) {
	t.Helper()
	if err := provider.Save(provider.Provider{ID: "kimi", Name: "Kimi Code", Preset: "kimi-code-cn", Key: "sk-first",
		Keys:   []provider.KeyAccount{{Key: "sk-second"}},
		Chat:   k.URL + "/coding/v1",
		Models: []string{"kimi-for-coding"}}); err != nil {
		t.Fatal(err)
	}
	st := settings.Load()
	st.Searcher = "kimi"
	if err := settings.Save(st); err != nil {
		t.Fatal(err)
	}
}

// With the plan's first key alias-isolated and a second ordinary key,
// magpie's own ordinary search still happens, on the ordinary key only.
func TestRoutingAliasKimiNamedSearchUsesOrdinaryKeyOnly(t *testing.T) {
	fresh(t)
	k := newRoutingAliasKimiPlan(t)
	routingAliasKimiNamed(t, k)
	// the service works as it is: with no alias the search goes out and
	// comes back, through the first key
	said, hits, err := New().webSearch(context.Background(), "latest go")
	if err != nil || len(hits) != 1 || hits[0].URL != "https://go.dev/dl/" || !strings.Contains(said, "go1.27.1") {
		t.Fatalf("baseline search: %q %+v %v", said, hits, err)
	}
	if got := k.take(); len(got) != 1 || got[0] != "sk-first" {
		t.Fatalf("baseline searched with %v; want exactly the first key while it is ordinary", got)
	}
	// the first key is kept for qualified references only
	if err := provider.SetKeyRoutingAlias("kimi", provider.KeyID("sk-first"), "work"); err != nil {
		t.Fatalf("setting the alias: %v", err)
	}
	said, hits, err = New().webSearch(context.Background(), "latest go")
	if err != nil || len(hits) != 1 || !strings.Contains(said, "go1.27.1") {
		t.Fatalf("ordinary search must still succeed on the ordinary key: %q %+v %v", said, hits, err)
	}
	if got := k.take(); len(got) != 1 || got[0] != "sk-second" {
		t.Errorf("ordinary web search spent keys %v; want only the ordinary key sk-second", got)
	}
}

// With every key of the plan alias-isolated, an ordinary search makes no
// call to the search service at all: the models exemption the service
// enjoys (searcherSpent) does not lift account isolation.
func TestRoutingAliasKimiNamedSearchAllKeysAliasedZeroUpstream(t *testing.T) {
	fresh(t)
	k := newRoutingAliasKimiPlan(t)
	routingAliasKimiNamed(t, k)
	// the service works as it is, before any alias exists
	if _, _, err := New().webSearch(context.Background(), "latest go"); err != nil {
		t.Fatalf("baseline search: %v", err)
	}
	if got := k.take(); len(got) != 1 || got[0] != "sk-first" {
		t.Fatalf("baseline searched with %v", got)
	}
	for key, alias := range map[string]string{"sk-first": "work", "sk-second": "team"} {
		if err := provider.SetKeyRoutingAlias("kimi", provider.KeyID(key), alias); err != nil {
			t.Fatalf("aliasing %s: %v", key, err)
		}
	}
	if _, _, err := New().webSearch(context.Background(), "latest go"); err == nil {
		t.Error("a search that reached no key was reported as made")
	}
	if got := k.take(); len(got) != 0 {
		t.Errorf("ordinary web search with every key isolated reached the search service with %v; want zero upstream calls", got)
	}
}
