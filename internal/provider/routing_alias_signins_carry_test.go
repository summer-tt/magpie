package provider

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/filememo"
	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/settings"
)

// The same portable fields used by backup.Bundle, without importing backup
// back into provider. Keep the whole JSON object so new sign-in metadata is
// exercised through its public encoding rather than reconstructed by a test.
type routingCarryData struct {
	Providers []Provider `json:"providers,omitempty"`
	SignIns   *SignIns   `json:"signIns,omitempty"`
}

func routingCarryJSON(t *testing.T, value any, out any) {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, out); err != nil {
		t.Fatal(err)
	}
}

func routingCarryWrite(t *testing.T, name string, value any) {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(settings.Dir(), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(settings.Dir(), name), raw, 0600); err != nil {
		t.Fatal(err)
	}
	filememo.Forget()
	catalog.Touched()
}

func routingCarryCollect(t *testing.T) routingCarryData {
	t.Helper()
	ps, err := Stored()
	if err != nil {
		t.Fatal(err)
	}
	s, err := CollectSignIns()
	if err != nil {
		t.Fatal(err)
	}
	var out routingCarryData
	routingCarryJSON(t, routingCarryData{Providers: ps, SignIns: s}, &out)
	return out
}

func routingCarryApply(t *testing.T, mode string, data routingCarryData) (SignInsResult, error) {
	t.Helper()
	if mode == "restore" {
		if _, _, err := Restore(data.Providers); err != nil {
			return SignInsResult{}, err
		}
		return RestoreSignIns(data.SignIns)
	}
	if len(data.Providers) > 0 {
		if err := Mirror(data.Providers, nil); err != nil {
			return SignInsResult{}, err
		}
	}
	local, err := CollectSignIns()
	if err != nil {
		return SignInsResult{}, err
	}
	shared, _, _ := MergeSignIns(local, data.SignIns, nil)
	// Carry the merged server payload through JSON before applying it here.
	var roundTrip SignIns
	routingCarryJSON(t, shared, &roundTrip)
	_, plan, _ := MergeSignIns(local, &roundTrip, nil)
	return ApplySignIns(plan)
}

func routingCarryRegistry(t *testing.T, id, owner string, active bool) {
	t.Helper()
	routingCarryWrite(t, "providers.json", map[string]any{"providers": []any{map[string]any{
		"id": id, "accountAliases": map[string]any{
			"work":    map[string]any{"accountId": owner, "active": active},
			"retired": map[string]any{"accountId": owner, "active": false},
		},
	}}})
}

// Use the cached fake provider from the existing slot-rename tests. Discovery
// creates the plugin:acme namespace and Home entries itself; no saved ID is set.
func routingCarryPlugin(t *testing.T, label string, publishAuth bool) {
	t.Helper()
	const spec = "@example/opencode-acme-auth"
	routingCarryWrite(t, "plugins.json", map[string]any{"plugins": []any{map[string]any{"spec": spec}}})
	if publishAuth {
		routingCarryWrite(t, "plugin-auth.json", map[string]any{
			"acme":   map[string]any{"type": "oauth", "refresh": "refresh-one", "access": "access-one", "expires": 9e15, "accountId": label},
			"acme#2": map[string]any{"type": "oauth", "refresh": "refresh-two", "access": "access-two", "expires": 9e15, "accountId": "other@fake"},
		})
	}
	plugin.UseCached([]plugin.Provider{{ID: "acme", Spec: spec, Name: "Acme", Methods: []plugin.Method{{Type: "oauth", Label: "Sign in"}}, Accounts: []plugin.Account{
		{Key: "acme", Type: "oauth", AccountID: label, Models: []string{"m"}},
		{Key: "acme#2", Type: "oauth", AccountID: "other@fake", Models: []string{"m"}},
	}, Models: listedModels(t, `[{"id":"m","name":"Model"}]`)}})
	catalog.Touched()
	pluginLogins(mustCached(t, "acme"))
}

func routingCarrySlot(t *testing.T, slot string) savedLogin {
	t.Helper()
	for _, l := range savedNow(t) {
		if l.Agent == "plugin:acme" && l.Home == slot {
			return l
		}
	}
	t.Fatalf("plugin namespace/Home slot %q missing", slot)
	return savedLogin{}
}

func TestRoutingAliasSignInsCarryPluginRenamedSlot(t *testing.T) {
	for _, mode := range []string{"restore", "sync"} {
		t.Run(mode, func(t *testing.T) {
			claudeHome(t)
			t.Setenv("MAGPIE_BUN", "")
			t.Cleanup(func() { plugin.UseCached(nil) })
			routingCarryPlugin(t, "before@fake", true)
			original := routingCarrySlot(t, "acme")
			if original.ID == "" {
				t.Fatal("normal discovery did not save an ID")
			}
			routingCarryRegistry(t, "acme", original.ID, true)
			registry := routingAliasReadRow(t, "acme")["accountAliases"]
			routingCarryPlugin(t, "after@fake", true)
			if got := routingCarrySlot(t, "acme"); got.ID != original.ID || got.User != "after@fake" {
				t.Fatalf("source rename did not retain saved owner: %+v", got)
			}
			data := routingCarryCollect(t)
			if len(data.SignIns.Plugins) != 2 {
				t.Fatal("export did not carry both plugin credentials")
			}

			// A separate machine has the plugin installed but no saved login or auth.
			plugin.UseCached(nil)
			claudeHome(t)
			if len(savedNow(t)) != 0 {
				t.Fatal("target sandbox already has saved accounts")
			}
			res, err := routingCarryApply(t, mode, data)
			if err != nil || res.Added != 2 {
				t.Fatalf("import did not restore credentials: %+v %v", res, err)
			}
			routingCarryPlugin(t, "after@fake", false)
			got := routingCarrySlot(t, "acme")
			if got.ID != original.ID {
				t.Errorf("restored renamed slot changed owner: got %s want %s", got.ID, original.ID)
			}
			if !reflect.DeepEqual(routingAliasReadRow(t, "acme")["accountAliases"], registry) {
				t.Error("restore changed active or retired alias history")
			}
			entries, err := plugin.AuthEntries()
			if err != nil {
				t.Fatal(err)
			}
			if credHash(entries["acme"]) != credHash(data.SignIns.Plugins["acme"]) {
				t.Error("original slot credential changed")
			}
			qualified, _, qualifiedOK := Resolve("acme/m@work")
			if !qualifiedOK || qualified.Account == nil || qualified.RoutingID() != original.ID || qualified.Account.User != "after@fake" {
				t.Errorf("qualified alias no longer resolves original credential owner: ok=%v account=%+v id=%s", qualifiedOK, qualified.Account, qualified.RoutingID())
			}
			ordinary, _, ok := Resolve("acme/m")
			if !ok || ordinary.Account == nil {
				t.Fatal("ordinary unaliased account unavailable")
			}
			if ordinary.RoutingAllows(got.ID) {
				t.Error("restored alias-reserved credential became eligible for ordinary routing")
			}
			other := routingCarrySlot(t, "acme#2")
			if !ordinary.RoutingAllows(other.ID) {
				t.Error("ordinary routing lost unaliased credential")
			}
			if qualifiedOK && qualified.Account != nil && qualified.RoutingAllows(other.ID) {
				t.Error("qualified alias admits another credential")
			}
		})
	}
}

// Each sandbox saves one Team workspace through rememberLogins. Both get the
// same display name and ID without the test assigning or clearing an ID.
func routingCarryCodex(t *testing.T, workspace string) savedLogin {
	t.Helper()
	home := signIn(t)
	t.Setenv("CODEX_HOME", "")
	codexWorkspaceSignIn(t, home, "carry@example.com", workspace, "refresh-"+workspace)
	rememberLogins(true)
	for _, l := range savedNow(t) {
		if l.Agent == "codex" {
			_, ws := codexWho(l.Auth)
			if ws == workspace {
				if l.ID == "" {
					t.Fatal("normal Codex save produced no ID")
				}
				return l
			}
		}
	}
	t.Fatalf("workspace %s not saved", workspace)
	return savedLogin{}
}

func TestRoutingAliasSignInsCarryCodexNoAlias(t *testing.T) {
	for _, mode := range []string{"restore", "sync"} {
		t.Run(mode, func(t *testing.T) {
			source := routingCarryCodex(t, "ws-b")
			data := routingCarryCollect(t)
			claudeHome(t)
			res, err := routingCarryApply(t, mode, data)
			if err != nil || res.Added < 1 {
				t.Fatalf("legal no-alias import did not add account: %+v %v", res, err)
			}
			for _, l := range savedNow(t) {
				if l.Agent == "codex" && credHash(l.Auth) == credHash(source.Auth) {
					t.Logf("legal import reached %s: workspace ws-b owner=%s added=%d", mode, l.ID, res.Added)
					return
				}
			}
			t.Fatal("legal import did not preserve incoming workspace credential")
		})
	}
}

func TestRoutingAliasSignInsCarryCodexOwnerCollision(t *testing.T) {
	for _, mode := range []string{"restore", "sync"} {
		for _, active := range []bool{true, false} {
			name := mode + "/retired"
			if active {
				name = mode + "/active"
			}
			t.Run(name, func(t *testing.T) {
				incoming := routingCarryCodex(t, "ws-b")
				data := routingCarryCollect(t)
				original := routingCarryCodex(t, "ws-a")
				if incoming.ID != original.ID || incoming.User != original.User {
					t.Fatalf("fixture must generate a real ID collision through normal saves: incoming=%+v original=%+v", incoming, original)
				}
				routingCarryRegistry(t, "codex", original.ID, active)
				before := savedNow(t)
				registry := routingAliasReadRow(t, "codex")["accountAliases"]
				// Import sign-ins only: the source has no provider registry to replace A's.
				res, err := routingCarryApply(t, mode, data)
				after := savedNow(t)
				if !reflect.DeepEqual(registry, routingAliasReadRow(t, "codex")["accountAliases"]) {
					t.Error("import changed reserved alias owner history")
				}
				if err != nil {
					if !reflect.DeepEqual(before, after) {
						t.Errorf("rejected import modified saved identities: %v", err)
					}
					t.Logf("collision safely rejected: %v", err)
					return
				}
				var foundA, foundB bool
				for _, l := range after {
					if l.Agent != "codex" {
						continue
					}
					_, ws := codexWho(l.Auth)
					switch ws {
					case "ws-a":
						foundA = true
						if l.ID != original.ID || credHash(l.Auth) != credHash(original.Auth) {
							t.Error("import rebound original workspace owner or credential")
						}
					case "ws-b":
						foundB = true
						if l.ID == original.ID {
							t.Errorf("imported workspace B shares alias-reserved owner %s with workspace A (result %+v)", l.ID, res)
						}
					}
				}
				if !foundA || !foundB {
					t.Errorf("successful import must retain A and add independent B, or explicitly reject: A=%v B=%v result=%+v", foundA, foundB, res)
				}
				if active {
					qualified, _, ok := Resolve("codex/gpt-5.5@work")
					if !ok || qualified.Account == nil {
						t.Fatal("original alias unavailable after import")
					}
					for _, l := range after {
						_, ws := codexWho(l.Auth)
						if l.Agent == "codex" && ws == "ws-b" && qualified.RoutingAllows(l.ID) {
							t.Error("qualified workspace A alias permits imported workspace B")
						}
					}
				}
			})
		}
	}
}
