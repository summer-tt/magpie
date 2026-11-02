package davsync

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/backup"
	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/provider"
)

func TestRoutingAliasSignInsCompatModernStateDeletion(t *testing.T) {
	fake := &fakeDAV{files: map[string][]byte{}, etags: map[string]string{}, dirs: map[string]bool{"/dav": true}}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	t.Cleanup(func() { plugin.UseCached(nil) })
	cfg := Config{URL: srv.URL + "/dav/", User: "me", Password: "pw", Passphrase: "fixture modern owner state", SignIns: true}
	publish := func(label, token string, expires int64) {
		t.Helper()
		const spec = "@example/opencode-acme-auth"
		dir := filepath.Dir(provider.Path())
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		for name, value := range map[string]any{
			"plugins.json":     map[string]any{"plugins": []any{map[string]any{"spec": spec}}},
			"plugin-auth.json": map[string]any{"acme": map[string]any{"type": "oauth", "accountId": label, "access": token, "refresh": "refresh-" + token, "expires": expires}},
		} {
			raw, err := json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(filepath.Join(dir, name), raw, 0600); err != nil {
				t.Fatal(err)
			}
		}
		plugin.UseCached([]plugin.Provider{{ID: "acme", Spec: spec, Name: "Acme", Methods: []plugin.Method{{Type: "oauth", Label: "Sign in"}}, Models: []plugin.Model{{ID: "m", Name: "Model"}}, Accounts: []plugin.Account{{Key: "acme", Type: "oauth", AccountID: label, Models: []string{"m"}}}}})
		provider.Changed()
		if ls := provider.Logins("plugin:acme"); len(ls) != 1 {
			t.Fatalf("normal discovery found %d accounts", len(ls))
		}
	}
	now := func() {
		t.Helper()
		if err := Now(context.Background()); err != nil {
			t.Fatal(err)
		}
		if status := Status(); status.Error != "" {
			t.Fatal(status.Error)
		}
	}
	source, target := newComputer(t), newComputer(t)
	source.use(t)
	publish("source-first@fake", "new", 5000000000000)
	sourceOwner := provider.LoginID("plugin:acme", "source-first@fake")
	publish("same@fake", "new", 5000000000000)
	if err := Configure(cfg); err != nil {
		t.Fatal(err)
	}
	now()

	target.use(t)
	publish("target-first@fake", "old", 4000000000000)
	targetOwner := provider.LoginID("plugin:acme", "target-first@fake")
	publish("same@fake", "old", 4000000000000)
	if sourceOwner == targetOwner {
		t.Fatal("independent normal saves did not produce different owners")
	}
	if err := Configure(cfg); err != nil {
		t.Fatal(err)
	}
	now()
	if !t.Run("first-sync", func(t *testing.T) {
		entries, err := plugin.AuthEntries()
		if err != nil {
			t.Fatal(err)
		}
		var auth struct {
			Access string `json:"access"`
		}
		if err = json.Unmarshal(entries["acme"], &auth); err != nil {
			t.Fatal(err)
		}
		if auth.Access != "new" {
			t.Error("real sync did not apply newer credential")
		}
		if got := provider.LoginID("plugin:acme", "same@fake"); got != targetOwner {
			t.Error("real sync did not retain local owner")
		}
		t.Logf("real first sync applied newer credential and persisted sync state: source=%s target=%s records=%d", sourceOwner, targetOwner, len(loadState().SignIns))
	}) {
		return
	}

	t.Run("peer-deletion", func(t *testing.T) {
		source.use(t)
		if err := plugin.EditAuth(func(entries map[string]json.RawMessage) (bool, error) { delete(entries, "acme"); return true, nil }); err != nil {
			t.Fatal(err)
		}
		now()
		readServer := func() backup.Bundle {
			t.Helper()
			fake.mu.Lock()
			raw := append([]byte(nil), fake.files["/dav/magpie/magpie.magpie-backup"]...)
			fake.mu.Unlock()
			b, err := backup.Open(raw, cfg.Passphrase)
			if err != nil {
				t.Fatal(err)
			}
			return b
		}
		deleted := readServer()
		if deleted.SignIns == nil || len(deleted.SignIns.Plugins) != 0 {
			t.Fatal("source deletion did not reach sealed server bundle")
		}
		target.use(t)
		now()
		entries, err := plugin.AuthEntries()
		if err != nil {
			t.Fatal(err)
		}
		if _, present := entries["acme"]; present {
			t.Error("real sync retained unchanged target credential after modern peer deletion")
		}
		final := readServer()
		if final.SignIns != nil && len(final.SignIns.Plugins) != 0 {
			t.Error("real sync reuploaded deleted modern credential into sealed server bundle")
		}
	})
}
