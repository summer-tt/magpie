package provider

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/filememo"
	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/settings"
)

// The rebase meet this file holds: upstream kept a plugin slot's cached
// account labels rebuilt from plugin-auth.json, while the alias feature
// made a qualified selection name its account by the saved login's stable
// id. A Provider snapshot the caller holds across a vendor's label change
// must keep naming the saved slot (the login's Home), not the label it was
// resolved under — logins.json may by then have been settled to the new
// label by another account list, so a label-keyed lookup finds no row and
// answers with a stand-in id that no alias is bound to.
func TestRoutingAliasPluginHeldSnapshotKeepsSavedOwner(t *testing.T) {
	claudeHome(t)
	t.Setenv("MAGPIE_BUN", "")
	spec := "@example/opencode-acme-auth"
	write := func(name string, value any) {
		t.Helper()
		b, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.MkdirAll(settings.Dir(), 0700); err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(settings.Dir(), name), b, 0600); err != nil {
			t.Fatal(err)
		}
		filememo.Forget()
		catalog.Touched()
	}
	write("plugins.json", map[string]any{"plugins": []map[string]any{{"spec": spec}}})
	publish := func(user string) {
		// Cached account labels are rebuilt from plugin-auth.json. OAuth
		// accountId is the vendor label; its map key is the stable slot.
		write("plugin-auth.json", map[string]any{
			"acme":   map[string]any{"type": "oauth", "refresh": "refresh-one", "access": "access-one", "expires": 9e15, "accountId": user},
			"acme#2": map[string]any{"type": "oauth", "refresh": "refresh-two", "access": "access-two", "expires": 9e15, "accountId": "other@fake"},
		})
		plugin.UseCached([]plugin.Provider{{ID: "acme", Spec: spec, Name: "Acme", Methods: []plugin.Method{{Type: "oauth", Label: "Sign in"}}, Accounts: []plugin.Account{{Key: "acme", Type: "oauth", AccountID: user, Models: []string{"m"}}, {Key: "acme#2", Type: "oauth", AccountID: "other@fake", Models: []string{"m"}}}, Models: listedModels(t, `[{"id":"m","name":"Model"}]`)}})
		catalog.Touched()
	}
	t.Cleanup(func() { plugin.UseCached(nil) })
	publish("before@fake")
	if p, _, ok := Resolve("acme/m"); !ok || p.Account == nil {
		t.Fatal("baseline cached plugin does not resolve")
	}
	// Assign the saved identities after initial slot discovery, as sign-in does.
	saveLogins(t, savedLogin{ID: "saved-slot-one", Agent: "plugin:acme", User: "before@fake", Home: "acme", On: true}, savedLogin{ID: "saved-slot-two", Agent: "plugin:acme", User: "other@fake", Home: "acme#2", On: true})
	write("providers.json", map[string]any{"providers": []any{map[string]any{"id": "acme", "accountAliases": map[string]any{"work": map[string]any{"accountId": "saved-slot-one", "active": true}}}}})

	// Warm up on the label as it is now: the qualified reference legally
	// resolves to the bound slot and names its saved id.
	held, _, ok := Resolve("acme/m@work")
	if !ok || held.Account == nil || held.Account.User != "before@fake" {
		t.Fatalf("baseline qualified resolve: ok=%v account=%+v", ok, held.Account)
	}
	if id := held.RoutingID(); id != "saved-slot-one" {
		t.Fatalf("baseline stable id: got %s, want saved-slot-one", id)
	}

	// The vendor renames the same slot's label; another account list
	// settles logins.json to it while this snapshot is held.
	publish("after@fake")
	pluginLogins(mustCached(t, "acme"))

	// The held snapshot keeps the saved identity and stays dispatchable.
	if id := held.RoutingID(); id != "saved-slot-one" {
		t.Errorf("held snapshot id after its slot's label change: got %s, want saved-slot-one", id)
	}
	if !held.RoutingCurrent() {
		t.Errorf("held snapshot was refused after its slot's label change: %s@work no longer current", held.ID)
	}
	// A fresh ordinary snapshot still keeps the aliased account out of the
	// ordinary pool, and the qualified reference still selects that owner.
	ordinary, _, ok := Resolve("acme/m")
	if !ok || ordinary.Account == nil {
		t.Fatalf("ordinary resolve after rename: ok=%v account=%+v", ok, ordinary.Account)
	}
	if ordinary.RoutingAllows("saved-slot-one") {
		t.Errorf("aliased account stayed in the ordinary pool after rename")
	}
	if !ordinary.RoutingAllows("saved-slot-two") {
		t.Errorf("the unaliased account was barred from the ordinary pool after rename")
	}
	fresh, _, ok := Resolve("acme/m@work")
	if !ok || fresh.Account == nil || fresh.Account.User != "after@fake" {
		t.Fatalf("qualified resolve after rename: ok=%v account=%+v", ok, fresh.Account)
	}
	if fresh.RoutingID() != held.RoutingID() {
		t.Errorf("qualified owner followed the label: fresh=%s held=%s, want saved-slot-one for both", fresh.RoutingID(), held.RoutingID())
	}
}
