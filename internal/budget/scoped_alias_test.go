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

// A limit scoped by a routing alias keeps counting the model when the
// alias goes — cleared, which leaves a retired binding in the registry,
// or its key gone while the ordinary one still serves the model: the
// scope stays the base model's, what the window counted stays counted
// and a base request is refused by the same limit, in the sums kept in
// memory and a restart's fresh read alike, and a reservation taken
// under the scope is kept whole and given back.
func TestScopedLimitKeepsCountingWhenTheAliasGoes(t *testing.T) {
	for _, tc := range []string{"alias cleared", "bound key gone", "native model collision"} {
		t.Run(tc, func(t *testing.T) {
			who := aliasKey(t, access.Limit{Period: "day", Tokens: 700, Models: []string{"alias-relay/m@work"}})
			key := access.Key{ID: who.KeyID, Limit: who.Limit, More: who.More}
			now := time.Now()
			// a request in flight: a 200-token reservation under the scope
			release, r := Reserve(who, 800, "alias-relay/m@work", now)
			if r != nil {
				t.Fatal(r)
			}
			// its window's calls settle at the cap
			for range 2 {
				Append(usage.Record{Time: now, Provider: "alias-relay", Model: "m", Requested: "alias-relay/m@work", Input: 350, Status: 200, CallerKeyID: who.KeyID})
			}
			switch tc {
			case "alias cleared":
				if err := provider.SetKeyRoutingAlias("alias-relay", provider.KeyID("k-work"), ""); err != nil {
					t.Fatal(err)
				}
			case "bound key gone", "native model collision":
				p, err := provider.Find("alias-relay")
				if err != nil {
					t.Fatal(err)
				}
				if tc == "bound key gone" {
					p.Keys = nil
				} else {
					p.Models = append(p.Models, "m@work")
				}
				if err := provider.Save(*p); err != nil {
					t.Fatal(err)
				}
			}
			// what the window counted and holds is kept, hot as it is
			all := All(key, now)
			if len(all) != 1 || all[0].Tokens != 700 || !all[0].Spent || all[0].InFlight != 1 || all[0].Reserved != 200 {
				t.Fatalf("%s: status %+v", tc, all)
			}
			if release, r := Reserve(who, -1, "alias-relay/m", now); r == nil {
				release()
				t.Fatalf("%s: a base request was let in past the cap", tc)
			} else if !strings.Contains(r.Error(), "alias-relay/m@work") {
				t.Fatalf("%s: a base request refused by another limit: %q", tc, r)
			}
			// the reservation is given back whole
			release()
			if all = All(key, now); len(all) != 1 || all[0].InFlight != 0 || all[0].Reserved != 0 {
				t.Fatalf("%s: status after the release %+v", tc, all)
			}
			// and a restart's fresh read of the ledger counts the same
			Forget()
			all = All(key, time.Now())
			if len(all) != 1 || all[0].Tokens != 700 || !all[0].Spent {
				t.Fatalf("%s: status after a restart %+v", tc, all)
			}
			if release, r := Reserve(who, -1, "alias-relay/m", time.Now()); r == nil {
				release()
				t.Fatalf("%s: a base request was let in past the cap after a restart", tc)
			}
			sp := Spent(who, time.Now())
			if len(sp) != 1 || !access.ModelAllowed(sp[0].Models, "alias-relay/m") || sp[0].Reset.IsZero() {
				t.Fatalf("%s: spent %+v", tc, sp)
			}
		})
	}
}

// A status read before an alias exists must not keep its literal scope
// after registration makes the same scope count base-model records. Both
// records already on disk and calls since registration count on the next
// read, through All and the batched AllOf.
func TestScopedStatusCountsAfterRoutingAliasRegistration(t *testing.T) {
	for _, batched := range []bool{false, true} {
		t.Run(map[bool]string{false: "All", true: "AllOf"}[batched], func(t *testing.T) {
			who := aliasKey(t, access.Limit{Period: "day", Tokens: 700, Models: []string{"alias-relay/m@later"}})
			key := access.Key{ID: who.KeyID, Limit: who.Limit, More: who.More}
			now := time.Now()
			statuses := func() []Status {
				if batched {
					return AllOf([]access.Key{key}, now)[0]
				}
				return All(key, now)
			}
			call := func() {
				Append(usage.Record{Time: now, Provider: "alias-relay", Model: "m", Input: 350, Status: 200, CallerKeyID: who.KeyID})
			}
			call()
			if got := statuses(); len(got) != 1 || got[0].Tokens != 0 {
				t.Fatalf("unbound alias counted base usage: %+v", got)
			}
			if err := provider.SetKeyRoutingAlias("alias-relay", provider.KeyID("k-work"), "later"); err != nil {
				t.Fatal(err)
			}
			call()
			for range 2 {
				if got := statuses(); len(got) != 1 || got[0].Tokens != 700 || got[0].Calls != 2 || !got[0].Spent {
					t.Fatalf("registered alias did not recount base usage: %+v", got)
				}
			}
		})
	}
}

// A scope's "@" is not trimmed as a matter of string handling: a native
// number marker ("alias-relay/m@500k") stays the literal model it names
// — its records count, the base model's don't, and a base request is
// not held by it — and an alias never bound counts nothing, as its
// requests fail resolution on their own.
func TestScopedLimitOfANativeAtAndAnUnboundAliasStayLiteral(t *testing.T) {
	who := aliasKey(t,
		access.Limit{Period: "day", Tokens: 700, Models: []string{"alias-relay/m@500k"}},
		access.Limit{Period: "day", Tokens: 700, Models: []string{"alias-relay/m@nope"}},
		access.Limit{Period: "day", Tokens: 700, Models: []string{"alias-relay/m@latest"}})
	p, err := provider.Find("alias-relay")
	if err != nil {
		t.Fatal(err)
	}
	p.Models = append(p.Models, "m@latest")
	if err := provider.Save(*p); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	call := func(model string, n int) {
		t.Helper()
		Append(usage.Record{Time: now, Provider: "alias-relay", Model: model, Input: n, Status: 200, CallerKeyID: who.KeyID})
	}
	call("m", 350)
	call("m@500k", 350)
	call("m@latest", 350)
	all := All(access.Key{ID: who.KeyID, Limit: who.Limit, More: who.More}, now)
	by := map[string]Status{}
	for _, st := range all {
		by[strings.Join(st.Models, ",")] = st
	}
	if len(by) != 3 || by["alias-relay/m@500k"].Tokens != 350 || by["alias-relay/m@latest"].Tokens != 350 || by["alias-relay/m@nope"].Tokens != 0 {
		t.Fatalf("statuses %+v", all)
	}
	// the base model's calls are the native marker's scope's neither
	if release, r := Reserve(who, -1, "alias-relay/m", now); r != nil {
		t.Fatal("a base request was held by a native marker's scope:", r)
	} else {
		release()
	}
	if sp := Spent(who, now); len(sp) != 0 {
		t.Fatalf("spent %+v", sp)
	}
}
