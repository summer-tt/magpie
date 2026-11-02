package davsync

import (
	"bytes"
	"context"
	"encoding/json"
	"maps"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/backup"
	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

func TestRoutingAliasSignInsMetadata(t *testing.T) {
	for _, scenario := range []string{"credential-only", "owner-added", "owner-changed", "owner-added-server-resealed"} {
		t.Run(scenario, func(t *testing.T) {
			fake := &fakeDAV{files: map[string][]byte{}, etags: map[string]string{}, dirs: map[string]bool{"/dav": true, "/dav/magpie": true}, cond: true, putETag: true}
			srv := httptest.NewServer(fake)
			defer srv.Close()
			cfg := Config{URL: srv.URL + "/dav/", User: "me", Password: "pw", Passphrase: "metadata fixture passphrase", SignIns: true}
			a, b := newComputer(t), newComputer(t)
			var source, target state
			sync := func(st *state) {
				t.Helper()
				if err := syncOnce(context.Background(), cfg, st); err != nil {
					t.Fatal(err)
				}
			}
			remote := func() (backup.Bundle, []byte, int) {
				t.Helper()
				fake.mu.Lock()
				raw := bytes.Clone(fake.files["/dav/magpie/magpie.magpie-backup"])
				puts := fake.puts
				fake.mu.Unlock()
				bundle, err := backup.Open(raw, cfg.Passphrase)
				if err != nil {
					t.Fatal(err)
				}
				return bundle, raw, puts
			}
			a.use(t)
			credential := json.RawMessage(`{"type":"oauth","access":"fixture-access","refresh":"fixture-refresh","expires":2000000000000,"accountId":"owner@example.invalid"}`)
			if err := plugin.EditAuth(func(m map[string]json.RawMessage) (bool, error) {
				m["acme"] = credential
				return true, nil
			}); err != nil {
				t.Fatal(err)
			}
			if scenario == "owner-changed" {
				writeRoutingAliasSignInsOwner(t, "saved-owner-before")
			}
			before, err := provider.CollectSignIns()
			if err != nil {
				t.Fatal(err)
			}
			sync(&source)
			initial, sealed, puts := remote()
			if !maps.Equal(initial.SignIns.Hashes(), before.Hashes()) || !maps.Equal(source.SignIns, before.Hashes()) {
				t.Fatal("initial upload did not retain credential hashes on the server and in sync state")
			}
			if bytes.Contains(sealed, []byte("fixture-access")) {
				t.Fatal("credential uploaded without sealing")
			}
			if scenario != "credential-only" {
				writeRoutingAliasSignInsOwner(t, "saved-owner-after")
				after, err := provider.CollectSignIns()
				if err != nil {
					t.Fatal(err)
				}
				if !maps.Equal(before.Hashes(), after.Hashes()) || !bytes.Equal(before.Plugins["acme"], after.Plugins["acme"]) {
					t.Fatal("fixture changed a credential while adding routing owner metadata")
				}
				if scenario == "owner-added-server-resealed" {
					// A different valid sealed version makes sync read and merge the
					// remote even though its credential payload has not changed.
					resealed, err := backup.Seal(initial, cfg.Passphrase)
					if err != nil {
						t.Fatal(err)
					}
					d, err := newRemote(cfg)
					if err != nil {
						t.Fatal(err)
					}
					if _, err := d.put(context.Background(), resealed, source.Server.ETag); err != nil {
						t.Fatal(err)
					}
					_, _, puts = remote()
				}
				sync(&source)
				updated, _, gotPuts := remote()
				if gotPuts != puts+1 {
					t.Errorf("metadata-only sign-in change was not uploaded: DAV PUTs before=%d after=%d", puts, gotPuts)
				}
				assertRoutingAliasSignInsOwner(t, updated.SignIns, "saved-owner-after")
				if !maps.Equal(source.SignIns, before.Hashes()) {
					t.Error("metadata-only upload changed the credential-only sync baseline")
				}
			}
			b.use(t)
			sync(&target)
			restored, err := provider.CollectSignIns()
			if err != nil {
				t.Fatal(err)
			}
			if len(restored.Plugins["acme"]) == 0 || !maps.Equal(restored.Hashes(), before.Hashes()) || !maps.Equal(target.SignIns, before.Hashes()) {
				t.Fatal("joining computer did not restore the unchanged credential and its sync baseline")
			}
			if scenario != "credential-only" {
				assertRoutingAliasSignInsOwner(t, restored, "saved-owner-after")
			}
		})
	}
}

// Build logins.json from the public carried JSON shape, including the private
// savedLogin value, so collection and restore use the same persisted fields.
func writeRoutingAliasSignInsOwner(t *testing.T, id string) {
	t.Helper()
	data, err := json.Marshal(map[string]any{"pluginLogins": map[string]any{"acme": map[string]any{
		"id": id, "agent": "plugin:acme", "home": "acme", "user": "owner@example.invalid", "on": true,
	}}})
	if err != nil {
		t.Fatal(err)
	}
	var carried provider.SignIns
	if err := json.Unmarshal(data, &carried); err != nil {
		t.Fatal(err)
	}
	rows, err := json.Marshal([]any{carried.PluginLogins["acme"]})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(settings.Dir(), "logins.json"), rows, 0o600); err != nil {
		t.Fatal(err)
	}
}

func assertRoutingAliasSignInsOwner(t *testing.T, signins *provider.SignIns, id string) {
	t.Helper()
	if signins == nil {
		t.Error("sign-ins missing")
		return
	}
	owner, ok := signins.PluginLogins["acme"]
	if !ok || owner.ID != id || owner.Agent != "plugin:acme" || owner.Home != "acme" {
		t.Errorf("routing owner was not carried intact: want id=%q agent=plugin:acme home=acme; got %+v (present=%v)", id, owner, ok)
	}
}
