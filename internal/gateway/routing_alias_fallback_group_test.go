package gateway

import (
	"slices"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

func TestRoutingAliasOrdinaryFallbackKeepsExplicitGroupOwners(t *testing.T) {
	v, h, _ := routingAliasFixture(t)
	primary, err := provider.Find("alias-relay")
	if err != nil {
		t.Fatal(err)
	}
	if err = provider.Save(provider.Provider{ID: "fallback-owners", Name: "Fallback owners", Chat: primary.Chat, Key: "fallback-a", Keys: []provider.KeyAccount{{Key: "fallback-b"}}, Models: []string{"m"}}); err != nil {
		t.Fatal(err)
	}
	for _, pair := range []struct{ key, alias string }{{"fallback-a", "a"}, {"fallback-b", "b"}} {
		if err := provider.SetKeyRoutingAlias("fallback-owners", provider.KeyID(pair.key), pair.alias); err != nil {
			t.Fatal(err)
		}
	}
	if err := provider.SaveGroup(provider.Group{ID: "fallback-aliases", Name: "Fallback aliases", Members: []string{"fallback-owners/m@a", "fallback-owners/m@b"}, Routing: provider.Ordered}); err != nil {
		t.Fatal(err)
	}
	// Prove both explicit members are valid on this exact upstream and wire.
	for _, pair := range []struct{ alias, key string }{{"a", "fallback-a"}, {"b", "fallback-b"}} {
		w := routingAliasSend(h, "fallback-owners/m@"+pair.alias, "")
		if w.Code != 200 {
			t.Fatalf("baseline explicit member %s: %d %s", pair.alias, w.Code, w.Body.String())
		}
		if got := v.take(); !slices.Equal(got, []routingAliasCall{{pair.key, "m"}}) {
			t.Fatalf("baseline member owner/plain model=%v", got)
		}
	}
	if w := routingAliasSend(h, "group/fallback-aliases", ""); w.Code != 200 {
		t.Fatalf("baseline direct group: %d %s", w.Code, w.Body.String())
	}
	if got := v.take(); !slices.Equal(got, []routingAliasCall{{"fallback-a", "m"}}) {
		t.Fatalf("baseline group ordering=%v", got)
	}
	primary, err = provider.Find("alias-relay")
	if err != nil {
		t.Fatal(err)
	}
	primary.Fallback = []string{"group/fallback-aliases"}
	if err = provider.Save(*primary); err != nil {
		t.Fatal(err)
	}
	if w := routingAliasSend(h, "alias-relay/m", ""); w.Code != 200 {
		t.Fatalf("baseline ordinary primary: %d %s", w.Code, w.Body.String())
	}
	v.take()
	v.mu.Lock()
	v.refused["k-ordinary"] = 429
	v.refused["fallback-a"] = 429
	v.mu.Unlock()
	w := routingAliasSend(h, "alias-relay/m", "")
	got := v.take()
	if w.Code != 200 {
		t.Errorf("ordinary fallback to explicit alias group: status=%d body=%s", w.Code, w.Body.String())
	}
	want := []routingAliasCall{{"k-ordinary", "m"}, {"fallback-a", "m"}, {"fallback-b", "m"}}
	if !slices.Equal(got, want) {
		t.Errorf("ordinary fallback lost/reordered explicit alias owners: calls=%v want=%v", got, want)
	}
}
