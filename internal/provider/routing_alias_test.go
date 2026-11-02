package provider

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/filememo"
)

func routingAliasStoredFixture(t *testing.T) map[string]any {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	binding := map[string]any{"ownerId": KeyID("key-work"), "keyId": KeyID("key-work"), "active": true}
	row := map[string]any{"id": "alias-storage", "name": "Alias storage", "chat": "http://127.0.0.1:1/v1", "key": "key-first", "keys": []any{map[string]any{"key": "key-work"}}, "models": []string{"m"}, "keyAliases": map[string]any{"work": binding}, "accountAliases": map[string]any{"missing": map[string]any{"accountId": "saved-login-missing", "active": true}}}
	b, err := json.Marshal(map[string]any{"providers": []any{row}})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(filepath.Dir(Path()), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(Path(), b, 0600); err != nil {
		t.Fatal(err)
	}
	filememo.Forget()
	catalog.Touched()
	return row
}
func routingAliasReadRow(t *testing.T, id string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(Path())
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Providers []map[string]any `json:"providers"`
	}
	if err = json.Unmarshal(b, &cfg); err != nil {
		t.Fatal(err)
	}
	for _, r := range cfg.Providers {
		if r["id"] == id {
			return r
		}
	}
	t.Fatalf("missing provider %s", id)
	return nil
}
func TestRoutingAliasStoredRoundTrip(t *testing.T) {
	want := routingAliasStoredFixture(t)
	ps, err := Stored()
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(ps)
	if err != nil {
		t.Fatal(err)
	}
	var rows []map[string]any
	if err = json.Unmarshal(b, &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("Stored providers = %d", len(rows))
	}
	for _, field := range []string{"keyAliases", "accountAliases"} {
		if !reflect.DeepEqual(rows[0][field], want[field]) {
			t.Errorf("Stored lost %s: got %v want %v", field, rows[0][field], want[field])
		}
	}
}
func TestRoutingAliasRegistrySurvivesProviderEdits(t *testing.T) {
	for _, operation := range []string{"save", "key-display-name", "key-rotation", "key-removal", "provider-rename"} {
		t.Run(operation, func(t *testing.T) {
			want := routingAliasStoredFixture(t)
			id := "alias-storage"
			var err error
			switch operation {
			case "save":
				var p *Provider
				p, err = Find(id)
				if err == nil {
					p.Models = []string{"m", "m2"}
					err = Save(*p)
				}
			case "key-display-name":
				err = RenameKey(id, KeyID("key-work"), "Work display")
			case "key-rotation":
				_, _, err = ReplaceKey(id, KeyID("key-work"), "key-rotated")
			case "key-removal":
				err = RemoveKey(id, KeyID("key-work"))
			case "provider-rename":
				err = Rename(id, "alias-renamed")
				id = "alias-renamed"
			}
			if err != nil {
				t.Fatal(err)
			}
			got := routingAliasReadRow(t, id)
			if operation == "key-rotation" {
				want["keyAliases"].(map[string]any)["work"].(map[string]any)["keyId"] = KeyID("key-rotated")
			}
			for _, field := range []string{"keyAliases", "accountAliases"} {
				if !reflect.DeepEqual(got[field], want[field]) {
					t.Errorf("%s lost/changed %s: got %v want %v", operation, field, got[field], want[field])
				}
			}
		})
	}
}
func TestRoutingAliasRestoreKeepsLocalKeylessRegistry(t *testing.T) {
	for _, operation := range []string{"restore", "mirror"} {
		t.Run(operation, func(t *testing.T) {
			want := routingAliasStoredFixture(t)
			incoming := Provider{ID: "alias-storage", Name: "Changed on another machine", Chat: "http://127.0.0.1:1/v1", Models: []string{"m"}}
			var err error
			if operation == "restore" {
				_, _, err = Restore([]Provider{incoming})
			} else {
				err = Mirror([]Provider{incoming}, nil)
			}
			if err != nil {
				t.Fatal(err)
			}
			got := routingAliasReadRow(t, "alias-storage")
			if got["key"] != "key-first" {
				t.Errorf("keyless restore replaced local credentials: %v", got["key"])
			}
			for _, field := range []string{"keyAliases", "accountAliases"} {
				if !reflect.DeepEqual(got[field], want[field]) {
					t.Errorf("%s discarded local %s: %v", operation, field, got[field])
				}
			}
		})
	}
}
func TestRoutingAliasRestoreOwnerConflictIsAtomic(t *testing.T) {
	for _, operation := range []string{"restore", "mirror"} {
		t.Run(operation, func(t *testing.T) {
			row := routingAliasStoredFixture(t)
			before, err := os.ReadFile(Path())
			if err != nil {
				t.Fatal(err)
			}
			row["keyAliases"].(map[string]any)["work"] = map[string]any{"ownerId": KeyID("another-owner"), "keyId": KeyID("another-owner"), "active": true}
			b, _ := json.Marshal(row)
			var incoming Provider
			if err = json.Unmarshal(b, &incoming); err != nil {
				t.Fatal(err)
			}
			if operation == "restore" {
				_, _, err = Restore([]Provider{incoming})
			} else {
				err = Mirror([]Provider{incoming}, nil)
			}
			if err == nil {
				t.Error("conflicting alias owner was accepted; expected providers update rejection")
			}
			after, readErr := os.ReadFile(Path())
			if readErr != nil {
				t.Fatal(readErr)
			}
			if string(after) != string(before) {
				t.Error("owner conflict modified providers.json; rejection must be atomic")
			}
		})
	}
}
func TestRoutingAliasGroupPreservesTwoOwnersAndMissingMembers(t *testing.T) {
	routingAliasStoredFixture(t)
	members := []string{"alias-storage/m@work", "alias-storage/m@backup"}
	if err := SaveGroup(Group{Name: "Explicit aliases", Members: members, Routing: Ordered}); err != nil {
		t.Fatal(err)
	}
	gs, err := StoredGroups()
	if err != nil {
		t.Fatal(err)
	}
	if len(gs) != 1 || !reflect.DeepEqual(gs[0].Members, members) {
		t.Errorf("saved group aliases = %+v; want distinct complete references %v", gs, members)
	}
}
