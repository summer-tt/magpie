package provider

import (
	"encoding/json"
	"testing"

	"github.com/yetone/magpie/internal/plugin"
)

func TestRoutingAliasSignInsStateModernOwnerDeletion(t *testing.T) {
	claudeHome(t)
	t.Cleanup(func() { plugin.UseCached(nil) })
	routingCompatPublish(t, map[string]json.RawMessage{"acme": routingCompatAuth("source-first@fake", "new", 5000000000000)})
	sourceOwner := routingCarrySlot(t, "acme").ID
	routingCompatPublish(t, map[string]json.RawMessage{"acme": routingCompatAuth("same@fake", "new", 5000000000000)})
	remote := routingCompatCollect(t)
	plugin.UseCached(nil)
	claudeHome(t)
	routingCompatPublish(t, map[string]json.RawMessage{"acme": routingCompatAuth("target-first@fake", "old", 4000000000000)})
	targetOwner := routingCarrySlot(t, "acme").ID
	routingCompatPublish(t, map[string]json.RawMessage{"acme": routingCompatAuth("same@fake", "old", 4000000000000)})
	if sourceOwner == targetOwner {
		t.Fatal("normal independent saves did not generate different owners")
	}
	local := routingCompatCollect(t)
	_, plan, sharedHashes := MergeSignIns(local, remote, nil)
	if _, err := ApplySignIns(plan); err != nil {
		t.Fatal(err)
	}
	if !t.Run("first-sync", func(t *testing.T) {
		if credHash(routingCompatEntries(t)["acme"]) != credHash(remote.Plugins["acme"]) {
			t.Error("legal first sync did not update newer credential")
		}
		if got := routingCarrySlot(t, "acme").ID; got != targetOwner {
			t.Errorf("legal first sync replaced target owner: got %s want %s", got, targetOwner)
		}
		t.Logf("legal modern sync applied newer credential, source owner=%s target retained owner=%s", sourceOwner, targetOwner)
	}) {
		return
	}

	// davsync's bothSides records the returned shared hash for each record ID
	// that is present after collection; use that state, without inventing hashes.
	applied := routingCompatCollect(t)
	prev := map[string]string{}
	for id, hash := range sharedHashes {
		if _, present := applied.Hashes()[id]; present {
			prev[id] = hash
		}
	}
	t.Run("peer-deletion", func(t *testing.T) {
		shared, deletion, _ := MergeSignIns(applied, &SignIns{}, prev)
		if _, err := ApplySignIns(deletion); err != nil {
			t.Fatal(err)
		}
		if _, present := routingCompatEntries(t)["acme"]; present {
			t.Error("unchanged modern credential survived peer deletion after target owner was retained")
		}
		if len(shared.Plugins) != 0 {
			t.Error("modern peer deletion resurrected credential in shared upload payload")
		}
	})
}
