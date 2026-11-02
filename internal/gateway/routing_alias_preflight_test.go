package gateway

import (
	"slices"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// The prechecks a request runs before its plan — cap careful's usage
// reads, the careful fallback enumeration and the unmasked-route check —
// must follow the same selection conditions as the request itself: a
// direct qualified request only ever goes to its bound owner and never
// expands its provider's fallback, and an ordinary precheck leaves
// alias-isolated accounts alone. Nothing here reaches a real vendor:
// the usage reads are stood in for and the precheck fixtures sit on
// unroutable addresses.

// routingAliasPreflightFixture is a two-key local provider whose fallback
// names a remote provider, with its second key alias-isolated: the shape
// the careful and redaction prechecks read.
func routingAliasPreflightFixture(t *testing.T) provider.Provider {
	t.Helper()
	fresh(t)
	for _, p := range []provider.Provider{
		{ID: "near", Name: "Near", Key: "k-near", Keys: []provider.KeyAccount{{Key: "k-near-work"}}, Models: []string{"m"}, Chat: "http://127.0.0.1:1/v1", Unredacted: true, Fallback: []string{"spare/m"}},
		{ID: "spare", Name: "Spare", Key: "k-spare", Models: []string{"m"}, Chat: "https://api.example.invalid/v1"},
	} {
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}
	if err := provider.SetKeyRoutingAlias("near", provider.KeyID("k-near-work"), "work"); err != nil {
		t.Fatal(err)
	}
	near, err := provider.Find("near")
	if err != nil {
		t.Fatal(err)
	}
	return *near
}

// carefulOf enumerates the providers a request may go to. An ordinary
// request keeps p and its fallbacks; a direct qualified request, which
// never expands its provider's fallback, must enumerate only itself.
func TestRoutingAliasCarefulOfQualifiedSkipsFallback(t *testing.T) {
	near := routingAliasPreflightFixture(t)
	if ps, _ := carefulOf(near, "m", nil); len(ps) != 2 || ps[0].ID != "near" || ps[1].ID != "spare" {
		ids := make([]string, len(ps))
		for i, p := range ps {
			ids[i] = p.ID
		}
		t.Fatalf("ordinary carefulOf = %v; want near then its fallback spare", ids)
	}
	q, m, err := provider.ResolveModel("near/m@work")
	if err != nil || m != "m" {
		t.Fatalf("qualified resolve: %v %q", err, m)
	}
	ps, models := carefulOf(q, m, nil)
	if len(ps) != 1 || ps[0].ID != "near" || ps[0].RoutingAlias() != "work" || models[0] != "m" {
		ids := make([]string, len(ps))
		for i, p := range ps {
			ids[i] = p.ModelRef(models[i])
		}
		t.Errorf("qualified carefulOf = %v; a direct qualified request enumerates only its bound target, never a fallback it cannot take", ids)
	}
}

// The unmasked-route precheck keeps an ordinary request masked while a
// remote provider stands among its fallbacks; a direct qualified request
// can never reach that fallback, so its unmasked local target decides
// alone.
func TestRoutingAliasQualifiedUnmaskedIgnoresUnreachableFallback(t *testing.T) {
	near := routingAliasPreflightFixture(t)
	if unredactedRoute(near, "m", false, nil) {
		t.Fatal("control: an ordinary request that may fall back to a remote provider must stay masked")
	}
	q, _, err := provider.ResolveModel("near/m@work")
	if err != nil {
		t.Fatal(err)
	}
	if !unredactedRoute(q, "m", false, nil) {
		t.Error("a direct qualified request was masked for a provider fallback it can never take; only its bound local target matters")
	}
}

// readCareful reads again the usage of the capped accounts a careful
// request may go to. A qualified request reads only its bound owner's
// usage; an ordinary request, whose pool excludes the alias-isolated
// account, reads no isolated owner. The usage reads are stood in for
// (standInReads): each account's last reading is old enough to be read
// again, and reading it back makes it fresh, so the request itself goes
// through to the account routing picks.
func TestRoutingAliasCarefulReadsOnlyEligibleAccounts(t *testing.T) {
	for _, mode := range []string{"qualified", "ordinary"} {
		t.Run(mode, func(t *testing.T) {
			b := carefulPool(t, 50, 10, true)
			if err := provider.SetAccountCap("codex", "spare@example.com", 90); err != nil {
				t.Fatal(err)
			}
			me := provider.LoginID("codex", "me@example.com")
			if err := provider.SetAccountRoutingAlias("codex", me, "work"); err != nil {
				t.Fatal(err)
			}
			r := standInReads(t)
			stale := time.Now().Add(-10 * time.Minute)
			r.set("me@example.com", stale, false)
			r.set("spare@example.com", stale, false)
			// a direct map write, as cap_pool_test's again callbacks do: this
			// runs inside readUsageNow, which holds r.mu
			r.again = func(u string) { r.at[u] = time.Now() }
			model := "codex/gpt-5.5"
			want, wantTried := "spare@example.com", "acct-2"
			if mode == "qualified" {
				model, want, wantTried = "codex/gpt-5.5@work", "me@example.com", "acct-1"
			}
			code, body := postAs(t, New(), "", `{"model":"`+model+`","messages":[{"role":"user","content":"hi"}]}`)
			if asked := r.askedFor(); !slices.Equal(asked, []string{want}) {
				t.Errorf("careful usage reads = %v; want only %s", asked, want)
			}
			if tried, _ := b.seen(); code != 200 || tried != wantTried {
				t.Errorf("%s request: %d %s, tried %s; want %s answering", mode, code, body, tried, wantTried)
			}
		})
	}
}
