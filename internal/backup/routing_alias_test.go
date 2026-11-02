package backup

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/filememo"
	"github.com/yetone/magpie/internal/provider"
)

func TestRoutingAliasBackupBundleIncludesOnlyPortableBindings(t *testing.T) {
	home(t)
	if err := provider.Save(provider.Provider{ID: "alias-backup", Name: "Alias backup", Chat: "http://127.0.0.1:1/v1", Key: "synthetic-backup-key", Models: []string{"m"}}); err != nil {
		t.Fatal(err)
	}
	if b, err := Collect(true, "routing-alias-test"); err != nil || len(b.Providers) != 1 {
		t.Fatalf("baseline backup collection: providers=%d err=%v", len(b.Providers), err)
	}
	path := provider.Path()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err = json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	owner := provider.KeyID("synthetic-backup-key")
	keys := map[string]any{"work": map[string]any{"ownerId": owner, "keyId": owner, "active": true}, "retired": map[string]any{"ownerId": owner, "keyId": owner, "active": false}}
	accounts := map[string]any{"subscription": map[string]any{"accountId": "missing-on-other-machine", "active": true}}
	row := cfg["providers"].([]any)[0].(map[string]any)
	row["keyAliases"] = keys
	row["accountAliases"] = accounts
	raw, _ = json.Marshal(cfg)
	if err = os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	filememo.Forget()
	catalog.Touched()
	for _, withKeys := range []bool{true, false} {
		t.Run(map[bool]string{true: "with-keys", false: "without-keys"}[withKeys], func(t *testing.T) {
			b, err := Collect(withKeys, "routing-alias-test")
			if err != nil {
				t.Fatal(err)
			}
			sealed, err := Seal(b, "fixture passphrase")
			if err != nil {
				t.Fatal(err)
			}
			opened, err := Open(sealed, "fixture passphrase")
			if err != nil {
				t.Fatal(err)
			}
			if len(opened.Providers) != 1 {
				t.Fatalf("bundle providers=%d", len(opened.Providers))
			}
			raw, err := json.Marshal(opened.Providers[0])
			if err != nil {
				t.Fatal(err)
			}
			var carried map[string]any
			if err = json.Unmarshal(raw, &carried); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(carried["accountAliases"], accounts) {
				t.Errorf("portable account bindings=%v want=%v", carried["accountAliases"], accounts)
			}
			if withKeys {
				if !reflect.DeepEqual(carried["keyAliases"], keys) {
					t.Errorf("key bindings/history=%v want=%v", carried["keyAliases"], keys)
				}
			} else {
				if value, ok := carried["keyAliases"]; ok && value != nil {
					t.Errorf("keyless sealed bundle leaked owner registry=%v", value)
				}
				if key, _ := carried["key"].(string); key != "" {
					t.Error("keyless bundle retained key")
				}
			}
			// Restoring the keyless bundle onto this machine must keep its complete
			// original key registry, including retired owner history.
			if !withKeys {
				if _, err := Restore(opened, Parts{Providers: true}); err != nil {
					t.Fatal(err)
				}
				local, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				var restored map[string]any
				if err = json.Unmarshal(local, &restored); err != nil {
					t.Fatal(err)
				}
				r := restored["providers"].([]any)[0].(map[string]any)
				if !reflect.DeepEqual(r["keyAliases"], keys) {
					t.Errorf("keyless bundle restore lost local registry=%v", r["keyAliases"])
				}
			}
		})
	}
	// Portable metadata for a subscription carries an unresolved saved ID; no
	// logins.json or plugin credential file is created merely by collecting it.
	for _, name := range []string{"logins.json", "plugin-auth.json"} {
		if _, err := os.Stat(filepath.Join(filepath.Dir(path), name)); !os.IsNotExist(err) {
			t.Errorf("backup alias metadata unexpectedly created %s: %v", name, err)
		}
	}
}
