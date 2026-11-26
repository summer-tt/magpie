package davsync

import (
	"context"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// With ProviderSettingsHere on, a computer keeps its own routing settings
// while the keys come in and go up. Which account a routing alias is
// bound to is one of those settings, but its names are also the record of
// who owned each: a name one computer let go of is not given to another
// owner on the next (docs/plans/account-alias-design.md §12.4). The
// settings stay where they are set; the owners are kept on both.
func TestRoutingAliasOwnersSurviveProviderSettingsHere(t *testing.T) {
	f, srv := newFakeS3(t)
	ctx := context.Background()
	a, b := newComputer(t), newComputer(t)
	sync := func(c computer) {
		t.Helper()
		c.use(t)
		if err := Now(ctx); err != nil {
			t.Fatal(err)
		}
	}
	ownerOf := func(c computer, name string) (owner string, found bool) {
		t.Helper()
		c.use(t)
		p, err := provider.Find("relay")
		if err != nil {
			t.Fatal(err)
		}
		bd, found := p.AccountAliases[name]
		return bd.AccountID, found
	}

	// a let go of "work", an account's
	a.use(t)
	if err := provider.Save(provider.Provider{ID: "relay", Chat: "https://relay.example/v1", Key: "k-a",
		AccountAliases: map[string]provider.AccountAliasBinding{"work": {AccountID: "a-owner"}}}); err != nil {
		t.Fatal(err)
	}
	if err := Configure(f.config(srv)); err != nil {
		t.Fatal(err)
	}
	sync(a)

	// b joins with its own settings, "local" bound to an account of its own
	b.use(t)
	cfgB := f.config(srv)
	cfgB.ProviderSettingsHere = true
	if err := provider.Save(provider.Provider{ID: "relay", Chat: "https://relay.example/v1", Key: "k-b",
		AccountAliases: map[string]provider.AccountAliasBinding{"local": {AccountID: "b-owner", Active: true}}}); err != nil {
		t.Fatal(err)
	}
	if err := Configure(cfgB); err != nil {
		t.Fatal(err)
	}
	sync(b)

	// pulled: b's own binding stays as b set it, and a's name is still a's here
	b.use(t)
	if p, err := provider.Find("relay"); err != nil || p.Key != "k-a" {
		t.Fatalf("b did not take a's key: %v %+v", err, p)
	}
	if owner, found := ownerOf(b, "local"); !found || owner != "b-owner" {
		t.Fatalf("b's own binding was lost in the pull: owner %q found %v", owner, found)
	}
	if owner, found := ownerOf(b, "work"); !found || owner != "a-owner" {
		t.Fatalf("b forgot who owned work on a: owner %q found %v", owner, found)
	}
	if err := provider.SetKeyRoutingAlias("relay", provider.KeyID("k-a"), "work"); err == nil || !strings.Contains(err.Error(), "another") {
		t.Fatalf("b gave the name a's account owned to a key: err %v", err)
	}

	// pushed: b changes a key, which takes its providers part up; a keeps
	// the name b owns
	b.use(t)
	if p, err := provider.Find("relay"); err != nil {
		t.Fatal(err)
	} else if p.Key = "k-b2"; provider.Save(*p) != nil {
		t.Fatal("save")
	}
	sync(b)
	sync(a)
	if owner, found := ownerOf(a, "local"); !found || owner != "b-owner" {
		t.Fatalf("a forgot who owned local on b after b pushed: owner %q found %v", owner, found)
	}
	if owner, found := ownerOf(a, "work"); !found || owner != "a-owner" {
		t.Fatalf("a lost its own record of work: owner %q found %v", owner, found)
	}
}
