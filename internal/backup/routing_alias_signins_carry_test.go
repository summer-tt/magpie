package backup

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/filememo"
	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/provider"
)

// This package verifies the real sealed backup boundary, which provider's
// tests cannot import without a cycle. Discovery, not the fixture, assigns IDs.
func TestRoutingAliasSignInsCarrySealedPluginBackup(t *testing.T) {
	for _, withAlias := range []bool{false, true} {
		name := "no-alias"
		if withAlias {
			name = "reserved-owner"
		}
		t.Run(name, func(t *testing.T) {
			home(t)
			t.Cleanup(func() { plugin.UseCached(nil) })
			write := func(name string, value any) {
				t.Helper()
				raw, err := json.Marshal(value)
				if err != nil {
					t.Fatal(err)
				}
				p := filepath.Join(filepath.Dir(provider.Path()), name)
				if err = os.MkdirAll(filepath.Dir(p), 0700); err != nil {
					t.Fatal(err)
				}
				if err = os.WriteFile(p, raw, 0600); err != nil {
					t.Fatal(err)
				}
				filememo.Forget()
				provider.Changed()
				catalog.Touched()
			}
			publish := func(label string, auth bool) {
				t.Helper()
				const spec = "@example/opencode-acme-auth"
				write("plugins.json", map[string]any{"plugins": []any{map[string]any{"spec": spec}}})
				if auth {
					write("plugin-auth.json", map[string]any{
						"acme":   map[string]any{"type": "oauth", "access": "access-one", "refresh": "refresh-one", "expires": 9e15, "accountId": label},
						"acme#2": map[string]any{"type": "oauth", "access": "access-two", "refresh": "refresh-two", "expires": 9e15, "accountId": "other@fake"},
					})
				}
				plugin.UseCached([]plugin.Provider{{ID: "acme", Spec: spec, Name: "Acme", Methods: []plugin.Method{{Type: "oauth", Label: "Sign in"}}, Models: []plugin.Model{{ID: "m", Name: "Model"}}, Accounts: []plugin.Account{
					{Key: "acme", Type: "oauth", AccountID: label, Models: []string{"m"}},
					{Key: "acme#2", Type: "oauth", AccountID: "other@fake", Models: []string{"m"}},
				}}})
				provider.Changed()
				catalog.Touched()
				if ls := provider.Logins("plugin:acme"); len(ls) != 2 {
					t.Fatalf("normal discovery found %d accounts", len(ls))
				}
			}
			publish("before@fake", true)
			owner := provider.LoginID("plugin:acme", "before@fake")
			registry := map[string]any{
				"work":    map[string]any{"accountId": owner, "active": true},
				"retired": map[string]any{"accountId": owner, "active": false},
			}
			if withAlias {
				write("providers.json", map[string]any{"providers": []any{map[string]any{"id": "acme", "accountAliases": registry}}})
			}
			publish("after@fake", true)
			if id := provider.LoginID("plugin:acme", "after@fake"); id != owner {
				t.Fatalf("source label change lost ID: %s want %s", id, owner)
			}
			b, err := Collect(true, "routing-carry-test")
			if err != nil {
				t.Fatal(err)
			}
			if err = WithSignIns(&b); err != nil {
				t.Fatal(err)
			}
			if b.SignIns == nil || len(b.SignIns.Plugins) != 2 {
				t.Fatal("backup omitted plugin sign-ins")
			}
			sealed, err := Seal(b, "synthetic carry passphrase")
			if err != nil {
				t.Fatal(err)
			}
			opened, err := Open(sealed, "synthetic carry passphrase")
			if err != nil {
				t.Fatal(err)
			}

			plugin.UseCached(nil)
			home(t)
			if _, err := os.Stat(filepath.Join(filepath.Dir(provider.Path()), "logins.json")); !os.IsNotExist(err) {
				t.Fatalf("target already has saved logins: %v", err)
			}
			result, err := Restore(opened, Parts{Providers: true, SignIns: true})
			if err != nil || result.SignIns == nil || result.SignIns.Added != 2 {
				t.Fatalf("sealed backup did not restore credentials: %+v %v", result.SignIns, err)
			}
			publish("after@fake", false)
			restored := provider.LoginID("plugin:acme", "after@fake")
			entries, err := plugin.AuthEntries()
			if err != nil {
				t.Fatal(err)
			}
			for key, want := range opened.SignIns.Plugins {
				var gotJSON, wantJSON any
				if err := json.Unmarshal(entries[key], &gotJSON); err != nil {
					t.Fatal(err)
				}
				if err := json.Unmarshal(want, &wantJSON); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(gotJSON, wantJSON) {
					t.Errorf("restored credential %s differs", key)
				}
			}
			ordinary, _, ok := provider.Resolve("acme/m")
			if !ok || ordinary.Account == nil {
				t.Fatal("ordinary plugin model unavailable after restore")
			}
			if !withAlias {
				if !ordinary.RoutingAllows(restored) {
					t.Error("legal no-alias restore excluded incoming credential")
				}
				t.Log("real Collect/WithSignIns/Seal/Open/Restore imported both credentials without aliases")
				return
			}
			if restored != owner {
				t.Errorf("sealed backup changed saved owner: got %s want %s", restored, owner)
			}
			raw, err := os.ReadFile(provider.Path())
			if err != nil {
				t.Fatal(err)
			}
			var cfg struct {
				Providers []map[string]any `json:"providers"`
			}
			if err = json.Unmarshal(raw, &cfg); err != nil {
				t.Fatal(err)
			}
			if len(cfg.Providers) != 1 || !reflect.DeepEqual(cfg.Providers[0]["accountAliases"], registry) {
				t.Errorf("sealed restore changed active/retired owner history: %s", raw)
			}
			qualified, _, ok := provider.Resolve("acme/m@work")
			if !ok || qualified.Account == nil || qualified.Account.User != "after@fake" || qualified.RoutingID() != owner {
				t.Errorf("qualified alias lost original restored credential: ok=%v id=%s", ok, qualified.RoutingID())
			}
			if ordinary.RoutingAllows(restored) {
				t.Error("sealed backup released reserved credential to ordinary routing")
			}
		})
	}
}
