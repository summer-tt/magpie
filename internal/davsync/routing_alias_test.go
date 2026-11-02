package davsync

import (
	"encoding/json"
	"testing"

	"github.com/yetone/magpie/internal/backup"
	"github.com/yetone/magpie/internal/provider"
)

func TestRoutingAliasSyncHashCarriesOwnerHistory(t *testing.T) {
	decode := func(alias string, active bool) backup.Bundle {
		raw, _ := json.Marshal(map[string]any{"id": "relay", "key": "fixture-key", "keyAliases": map[string]any{alias: map[string]any{"ownerId": "original-owner", "keyId": provider.KeyID("fixture-key"), "active": active}}})
		var p provider.Provider
		if err := json.Unmarshal(raw, &p); err != nil {
			t.Fatal(err)
		}
		return backup.Bundle{Providers: []provider.Provider{p}}
	}
	active := decode("work", true)
	before, _ := json.Marshal(active)
	baseline := hashes(active)["providers"]
	if baseline == "" {
		t.Fatal("baseline providers hash empty")
	}
	for _, changed := range []backup.Bundle{decode("work", false), decode("renamed", true)} {
		if hashes(changed)["providers"] == baseline {
			t.Error("sync providers hash ignored alias active/history change")
		}
	}
	after, _ := json.Marshal(active)
	if string(before) != string(after) {
		t.Error("hashing changed source registry")
	}
}
