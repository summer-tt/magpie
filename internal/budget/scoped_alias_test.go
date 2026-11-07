package budget

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/usage"
)

// aliasKey is a key in a sandbox HOME with limits ls, and the provider of
// the gateway's alias fixture, "alias-relay": two keys, "work" an alias
// of the second, serving the one model m.
func aliasKey(t *testing.T, ls ...access.Limit) access.Identity {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	catalog.Reset()
	t.Cleanup(catalog.Reset)
	Forget()
	t.Cleanup(Forget)
	p := provider.Provider{ID: "alias-relay", Name: "Alias relay", Chat: "http://127.0.0.1:1/v1", Key: "k-ordinary", Keys: []provider.KeyAccount{{Key: "k-work"}}, Models: []string{"m"}}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	if err := provider.SetKeyRoutingAlias("alias-relay", provider.KeyID("k-work"), "work"); err != nil {
		t.Fatal(err)
	}
	if _, err := access.Update("add-key", access.Change{Name: "Shared"}); err != nil {
		t.Fatal(err)
	}
	ks, _ := access.List()
	for i := range ls {
		n := i
		if _, err := access.Update("limit-key", access.Change{Key: ks[0].ID, Limit: &ls[i], Index: &n}); err != nil {
			t.Fatal(err)
		}
	}
	ks, _ = access.List()
	k := ks[0]
	return access.Identity{KeyID: k.ID, KeyName: k.Name, Limit: k.Limit, More: k.More}
}

// A limit scoped to a model by a routing alias ("alias-relay/m@work",
// what "magpie gateway-key limit <key> day --tokens 700 --model
// alias-relay/m@work" saves) counts the model's calls, which the ledger
// records by the base ref whichever account answered, and refuses the
// next request once they reach the cap, as a limit scoped to the base
// ref always did; its spent scope names the base ref too, the id the
// gateway asks its spent limits by.
func TestScopedLimitCountsARoutingAliasModelAsItsBase(t *testing.T) {
	for _, scope := range []string{"alias-relay/m@work", "alias-relay/m"} {
		t.Run(scope, func(t *testing.T) {
			who := aliasKey(t, access.Limit{Period: "day", Tokens: 700, Models: []string{scope}})
			now := time.Now()
			// a request that names the alias is let in under the scope
			// while the window has something left
			if release, r := Reserve(who, -1, "alias-relay/m@work", now); r != nil {
				t.Fatal(r)
			} else {
				release()
			}
			// two 350-token calls: recorded by the base ref, the
			// qualified spelling kept in Requested
			for range 2 {
				Append(usage.Record{Time: now, Provider: "alias-relay", Model: "m", Requested: "alias-relay/m@work", Input: 350, Status: 200, CallerKeyID: who.KeyID})
			}
			all := All(access.Key{ID: who.KeyID, Limit: who.Limit, More: who.More}, now)
			if len(all) != 1 || all[0].Tokens != 700 || !all[0].Spent {
				t.Fatalf("status of %s: %+v", scope, all)
			}
			for _, model := range []string{"alias-relay/m@work", "alias-relay/m"} {
				if release, r := Reserve(who, -1, model, now); r == nil {
					release()
					t.Fatalf("%s: a request for %s was let in past the cap", scope, model)
				} else if !strings.Contains(r.Error(), scope) {
					t.Fatalf("%s: a request for %s refused as %q", scope, model, r)
				}
			}
			sp := Spent(who, now)
			if len(sp) != 1 || !access.ModelAllowed(sp[0].Models, "alias-relay/m") || access.ModelAllowed(sp[0].Models, "alias-relay/other") || sp[0].Reset.IsZero() {
				t.Fatalf("%s: spent %+v", scope, sp)
			}
			// the same again from the ledger, as a restart reads it
			Forget()
			if release, r := Reserve(who, -1, "alias-relay/m@work", time.Now()); r == nil {
				release()
				t.Fatalf("%s: let in past the cap after a restart", scope)
			}
		})
	}
}

// A limit scoped by a routing alias whose binding is gone counts
// nothing, as its requests fail resolution on their own, and the base
// model's calls are another scope's to count.
func TestScopedLimitOfAGoneAliasCountsNothing(t *testing.T) {
	who := aliasKey(t, access.Limit{Period: "day", Tokens: 700, Models: []string{"alias-relay/m@work"}})
	if err := provider.SetKeyRoutingAlias("alias-relay", provider.KeyID("k-work"), ""); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	Append(usage.Record{Time: now, Provider: "alias-relay", Model: "m", Input: 350, Status: 200, CallerKeyID: who.KeyID})
	all := All(access.Key{ID: who.KeyID, Limit: who.Limit, More: who.More}, now)
	if len(all) != 1 || all[0].Tokens != 0 {
		t.Fatalf("status %+v", all)
	}
}
