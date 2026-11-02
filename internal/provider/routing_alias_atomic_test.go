package provider

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/filememo"
)

func TestRoutingAliasRestoreMultipleProvidersAtomic(t *testing.T) {
	for _, operation := range []string{"restore", "mirror"} {
		t.Run(operation, func(t *testing.T) {
			first := routingAliasStoredFixture(t)
			b, _ := json.Marshal(first)
			var second map[string]any
			json.Unmarshal(b, &second)
			second["id"] = "second-storage"
			second["name"] = "Second storage"
			raw, _ := json.Marshal(map[string]any{"providers": []any{first, second}})
			if err := os.WriteFile(Path(), raw, 0600); err != nil {
				t.Fatal(err)
			}
			filememo.Forget()
			catalog.Touched()
			ps, err := Stored()
			if err != nil || len(ps) != 2 {
				t.Fatalf("baseline stored providers=%d err=%v", len(ps), err)
			}
			before, _ := os.ReadFile(Path())
			first["name"] = "Remote first changed"
			second["keyAliases"].(map[string]any)["work"] = map[string]any{"ownerId": KeyID("different-owner"), "keyId": KeyID("different-owner"), "active": true}
			incoming, _ := json.Marshal([]any{first, second})
			if err = json.Unmarshal(incoming, &ps); err != nil {
				t.Fatal(err)
			}
			if operation == "restore" {
				_, _, err = Restore(ps)
			} else {
				err = Mirror(ps, nil)
			}
			if err == nil {
				t.Error("second provider owner conflict accepted")
			}
			after, _ := os.ReadFile(Path())
			if !bytes.Equal(before, after) {
				t.Error("second provider conflict partially updated first provider")
			}
		})
	}
}
