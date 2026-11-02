package provider

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/plugin"
)

func routingCompatAuth(label, token string, expires int64) json.RawMessage {
	raw, _ := json.Marshal(map[string]any{"type": "oauth", "accountId": label, "access": token, "refresh": "refresh-" + token, "expires": expires})
	return raw
}

// Use normal cached-plugin discovery to assign each slot's saved owner, even
// when two machines first saw different labels for the same AuthWho.
func routingCompatPublish(t *testing.T, entries map[string]json.RawMessage) {
	t.Helper()
	const spec = "@example/opencode-acme-auth"
	routingCarryWrite(t, "plugins.json", map[string]any{"plugins": []any{map[string]any{"spec": spec}}})
	routingCarryWrite(t, "plugin-auth.json", entries)
	pp := plugin.Provider{ID: "acme", Spec: spec, Name: "Acme", Methods: []plugin.Method{{Type: "oauth", Label: "Sign in"}}, Models: listedModels(t, `[{"id":"m","name":"Model"}]`)}
	for key, raw := range entries {
		pp.Accounts = append(pp.Accounts, plugin.Account{Key: key, Type: "oauth", AccountID: plugin.AuthWho(raw), Models: []string{"m"}})
	}
	plugin.UseCached([]plugin.Provider{pp})
	catalog.Touched()
	pluginLogins(mustCached(t, "acme"))
}

func routingCompatEntries(t *testing.T) map[string]json.RawMessage {
	t.Helper()
	entries, err := plugin.AuthEntries()
	if err != nil {
		t.Fatal(err)
	}
	return entries
}

func routingCompatCollect(t *testing.T) *SignIns {
	t.Helper()
	s, err := CollectSignIns()
	if err != nil {
		t.Fatal(err)
	}
	var out SignIns
	routingCarryJSON(t, s, &out)
	return &out
}

func routingCompatApply(t *testing.T, mode string, remote *SignIns, prev map[string]string) error {
	t.Helper()
	if mode == "restore" {
		_, err := RestoreSignIns(remote)
		return err
	}
	local := routingCompatCollect(t)
	_, plan, _ := MergeSignIns(local, remote, prev)
	_, err := ApplySignIns(plan)
	return err
}

// Decode an actual legacy wire object, rather than setting a new metadata
// field in a test fixture. Its Hashes are the old credential-only hashes.
func routingCompatLegacy(t *testing.T, s *SignIns) *SignIns {
	t.Helper()
	var wire map[string]json.RawMessage
	routingCarryJSON(t, s, &wire)
	delete(wire, "pluginLogins")
	var legacy SignIns
	routingCarryJSON(t, wire, &legacy)
	return &legacy
}

func routingCompatUnchanged(t *testing.T, logins []savedLogin, auth map[string]json.RawMessage, registry any) {
	t.Helper()
	if !reflect.DeepEqual(savedNow(t), logins) {
		t.Error("rejected import changed saved identities")
	}
	if !reflect.DeepEqual(routingCompatEntries(t), auth) {
		t.Error("rejected import changed credentials")
	}
	if !reflect.DeepEqual(routingAliasReadRow(t, "acme")["accountAliases"], registry) {
		t.Error("rejected import changed alias history")
	}
}

func TestRoutingAliasSignInsCompatSameCredentialAddsOwner(t *testing.T) {
	for _, mode := range []string{"restore", "sync"} {
		t.Run(mode, func(t *testing.T) {
			claudeHome(t)
			t.Cleanup(func() { plugin.UseCached(nil) })
			routingCarryPlugin(t, "before@fake", true)
			owner := routingCarrySlot(t, "acme").ID
			routingCarryPlugin(t, "after@fake", true)
			remote := routingCompatCollect(t)
			auth := routingCompatEntries(t)
			plugin.UseCached(nil)
			claudeHome(t)
			routingCarryWrite(t, "plugin-auth.json", auth)
			routingCarryRegistry(t, "acme", owner, true)
			registry := routingAliasReadRow(t, "acme")["accountAliases"]
			if len(savedNow(t)) != 0 {
				t.Fatal("target already has a saved owner")
			}
			if err := routingCompatApply(t, mode, remote, nil); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(routingCompatEntries(t), auth) {
				t.Error("metadata-only import changed credential")
			}
			if got := routingCarrySlot(t, "acme"); got.ID != owner {
				t.Errorf("same credential did not acquire source stable owner: got %s want %s", got.ID, owner)
			}
			routingCarryPlugin(t, "after@fake", false)
			if !reflect.DeepEqual(routingAliasReadRow(t, "acme")["accountAliases"], registry) {
				t.Error("owner completion changed active/retired history")
			}
			ordinary, _, ok := Resolve("acme/m")
			if !ok || ordinary.RoutingAllows(owner) {
				t.Error("owner completion released reserved credential to ordinary routing")
			}
			qualified, _, ok := Resolve("acme/m@work")
			if !ok || qualified.Account == nil || qualified.RoutingID() != owner || qualified.Account.User != "after@fake" {
				t.Error("owner completion did not restore qualified credential")
			}
		})
	}
}

func TestRoutingAliasSignInsCompatLegacyPeer(t *testing.T) {
	for _, operation := range []string{"newer", "delete-with-old-hash"} {
		t.Run(operation, func(t *testing.T) {
			claudeHome(t)
			t.Cleanup(func() { plugin.UseCached(nil) })
			auth := map[string]json.RawMessage{"acme": routingCompatAuth("same@fake", "old", 4000000000000)}
			routingCompatPublish(t, auth)
			owner := routingCarrySlot(t, "acme").ID
			routingCarryRegistry(t, "acme", owner, true)
			registry := routingAliasReadRow(t, "acme")["accountAliases"]
			legacy := routingCompatLegacy(t, routingCompatCollect(t))
			if operation == "newer" {
				legacy.Plugins["acme"] = routingCompatAuth("same@fake", "new", 5000000000000)
				if err := routingCompatApply(t, "sync", legacy, nil); err != nil {
					t.Fatal(err)
				}
				if credHash(routingCompatEntries(t)["acme"]) != credHash(legacy.Plugins["acme"]) {
					t.Error("legacy newer credential was not applied")
				}
				if got := routingCarrySlot(t, "acme").ID; got != owner {
					t.Error("legacy newer update replaced local owner")
				}
			} else {
				if err := routingCompatApply(t, "sync", &SignIns{}, legacy.Hashes()); err != nil {
					t.Fatal(err)
				}
				if _, exists := routingCompatEntries(t)["acme"]; exists {
					t.Error("legacy credential-only prev hash did not delete unchanged local credential removed by peer")
				}
			}
			if !reflect.DeepEqual(routingAliasReadRow(t, "acme")["accountAliases"], registry) {
				t.Error("legacy peer update rebound alias history")
			}
		})
	}
}

func TestRoutingAliasSignInsCompatUnreservedOwners(t *testing.T) {
	for _, scenario := range []string{"same-slot", "remap-with-unrelated-alias", "reserved-remap-rejected"} {
		t.Run(scenario, func(t *testing.T) {
			claudeHome(t)
			t.Cleanup(func() { plugin.UseCached(nil) })
			other := routingCompatAuth("other@fake", "other", 4000000000000)
			routingCompatPublish(t, map[string]json.RawMessage{"acme": routingCompatAuth("source-first@fake", "new", 5000000000000), "acme#2": other})
			sourceOwner := routingCarrySlot(t, "acme").ID
			remoteAuth := map[string]json.RawMessage{"acme": routingCompatAuth("same@fake", "new", 5000000000000), "acme#2": other}
			routingCompatPublish(t, remoteAuth)
			remote := routingCompatCollect(t)

			plugin.UseCached(nil)
			claudeHome(t)
			key := "acme"
			if scenario != "same-slot" {
				key = "acme#7"
			}
			routingCompatPublish(t, map[string]json.RawMessage{key: routingCompatAuth("local-first@fake", "old", 4000000000000), "acme#2": other})
			localOwner := routingCarrySlot(t, key).ID
			localAuth := map[string]json.RawMessage{key: routingCompatAuth("same@fake", "old", 4000000000000), "acme#2": other}
			routingCompatPublish(t, localAuth)
			if sourceOwner == localOwner {
				t.Fatal("normal independent saves did not produce different owners")
			}
			if scenario == "remap-with-unrelated-alias" {
				routingCarryRegistry(t, "acme", routingCarrySlot(t, "acme#2").ID, true)
			}
			if scenario == "reserved-remap-rejected" {
				routingCarryRegistry(t, "acme", localOwner, true)
				before, auth, registry := savedNow(t), routingCompatEntries(t), routingAliasReadRow(t, "acme")["accountAliases"]
				if err := routingCompatApply(t, "sync", remote, nil); err == nil {
					t.Error("reserved slot remap was accepted")
				}
				routingCompatUnchanged(t, before, auth, registry)
				return
			}
			var registry any
			if scenario != "same-slot" {
				registry = routingAliasReadRow(t, "acme")["accountAliases"]
			}
			if err := routingCompatApply(t, "sync", remote, nil); err != nil {
				t.Fatalf("legal newer sync rejected: %v", err)
			}
			entries := routingCompatEntries(t)
			if credHash(entries[key]) != credHash(remoteAuth["acme"]) {
				t.Error("newer credential did not update destination slot")
			}
			if key != "acme" {
				if _, exists := entries["acme"]; exists {
					t.Error("remap duplicated incoming slot instead of updating local slot")
				}
			}
			if got := routingCarrySlot(t, key).ID; got != localOwner {
				t.Errorf("newer sync changed unreserved local owner: got %s want %s", got, localOwner)
			}
			if scenario != "same-slot" && !reflect.DeepEqual(routingAliasReadRow(t, "acme")["accountAliases"], registry) {
				t.Error("legal remap changed unrelated alias history")
			}
		})
	}
}

func routingCompatWorkspace(t *testing.T, workspace string) savedLogin {
	t.Helper()
	for _, l := range savedNow(t) {
		_, ws := codexWho(l.Auth)
		if l.Agent == "codex" && ws == workspace {
			return l
		}
	}
	t.Fatalf("normal save/import did not retain workspace %s", workspace)
	return savedLogin{}
}

func TestRoutingAliasSignInsCompatCodexFinalOwner(t *testing.T) {
	// Existing alias-free import semantics can leave a saved ID generated from
	// the original Team label while nameAlike distinguishes the second workspace.
	// Create that state through real saves and RestoreSignIns, never by assigning ID.
	sourceB := routingCarryCodex(t, "sharedws-source")
	routingCarryCodex(t, "source-base")
	if _, err := RestoreSignIns(&SignIns{Logins: []savedLogin{sourceB}}); err != nil {
		t.Fatal(err)
	}
	incoming := routingCompatWorkspace(t, "sharedws-source")
	if incoming.ID != sourceB.ID || incoming.User == sourceB.User {
		t.Fatal("alias-free import did not retain original ID while distinguishing workspace label")
	}

	home := signIn(t)
	t.Setenv("CODEX_HOME", "")
	codexWorkspaceSignIn(t, home, "carry@example.com", "target-base", "refresh-target-base")
	rememberLogins(true)
	unreserved := routingCompatWorkspace(t, "target-base")
	codexWorkspaceSignIn(t, home, "carry@example.com", "sharedws-target", "refresh-sharedws-target")
	rememberLogins(true)
	reserved := routingCompatWorkspace(t, "sharedws-target")
	if incoming.ID != unreserved.ID || incoming.User != reserved.User || incoming.ID == reserved.ID {
		t.Fatalf("fixture lacks incoming-ID then final-name collision: incoming=%+v unreserved=%+v reserved=%+v", incoming, unreserved, reserved)
	}
	routingCarryRegistry(t, "codex", reserved.ID, true)
	before := savedNow(t)
	registry := routingAliasReadRow(t, "codex")["accountAliases"]
	if _, err := RestoreSignIns(&SignIns{Logins: []savedLogin{incoming}}); err == nil {
		t.Error("Codex import accepted final generated ID belonging to another reserved workspace")
	}
	if !reflect.DeepEqual(savedNow(t), before) {
		t.Error("rejected final-owner collision changed local identities or credentials")
	}
	if !reflect.DeepEqual(routingAliasReadRow(t, "codex")["accountAliases"], registry) {
		t.Error("rejected final-owner collision changed alias registry")
	}
}

func TestRoutingAliasSignInsCompatCodexOrphanedRetiredOwner(t *testing.T) {
	incoming := routingCarryCodex(t, "ws-incoming")
	claudeHome(t)
	routingCarryRegistry(t, "codex", incoming.ID, false)
	registry := routingAliasReadRow(t, "codex")["accountAliases"]
	if len(savedNow(t)) != 0 {
		t.Fatal("orphan fixture already contains verifiable login")
	}
	if _, err := RestoreSignIns(&SignIns{Logins: []savedLogin{incoming}}); err == nil {
		t.Error("import accepted orphaned retired owner without verifiable local login")
	}
	if len(savedNow(t)) != 0 {
		t.Error("orphan rejection added a saved login")
	}
	if !reflect.DeepEqual(routingAliasReadRow(t, "codex")["accountAliases"], registry) {
		t.Error("orphan rejection rebound retired history")
	}
}
