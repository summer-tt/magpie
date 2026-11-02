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

// A plugin slot has a saved identity even when its vendor changes the label.
// Routing must follow that saved identity, rather than the old label's hash.
func TestRoutingAliasPluginSlotRenameKeepsSavedOwner(t *testing.T) {
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
	if id := LoginID("plugin:acme", "before@fake"); id != "saved-slot-one" {
		t.Fatalf("saved plugin slot setup: expected saved-slot-one, got %s", id)
	}
	write("providers.json", map[string]any{"providers": []any{map[string]any{"id": "acme", "accountAliases": map[string]any{"work": map[string]any{"accountId": "saved-slot-one", "active": true}}}}})
	for _, user := range []string{"before@fake", "after@fake"} {
		publish(user)
		// The source-of-truth login record must retain its id on this known slot.
		pluginLogins(mustCached(t, "acme"))
		if id := LoginID("plugin:acme", user); id != "saved-slot-one" {
			t.Fatalf("fixture slot acme identity after label %q: expected saved-slot-one, got %s", user, id)
		}
		p, model, ok := Resolve("acme/m@work")
		if !ok || p.Account == nil || p.Account.User != user || model != "m" {
			t.Errorf("qualified plugin slot %s: ok=%v model=%q; want original saved slot with plain model", user, ok, model)
		}
	}
}
