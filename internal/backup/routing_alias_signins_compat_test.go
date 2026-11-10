package backup

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/provider"
)

// Read saved results without constructing or assigning a provider-private ID.
type bundleCompatLogin struct {
	ID    string          `json:"id"`
	Agent string          `json:"agent"`
	User  string          `json:"user"`
	Auth  json.RawMessage `json:"auth"`
}

func bundleCompatSaved(t *testing.T) map[string]bundleCompatLogin {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(provider.Path()), "logins.json"))
	if err != nil {
		t.Fatal(err)
	}
	var rows []bundleCompatLogin
	if err = json.Unmarshal(raw, &rows); err != nil {
		t.Fatal(err)
	}
	out := map[string]bundleCompatLogin{}
	for _, row := range rows {
		if row.Agent != "codex" {
			continue
		}
		var auth struct {
			Tokens struct {
				AccountID string `json:"account_id"`
			} `json:"tokens"`
		}
		if err = json.Unmarshal(row.Auth, &auth); err != nil {
			t.Fatal(err)
		}
		out[auth.Tokens.AccountID] = row
	}
	return out
}

func bundleCompatMachine(t *testing.T, workspace string) bundleCompatLogin {
	t.Helper()
	home(t)
	codexDir := filepath.Join(os.Getenv("HOME"), ".codex")
	if err := os.MkdirAll(codexDir, 0700); err != nil {
		t.Fatal(err)
	}
	jwt := func(claims any) string {
		raw, err := json.Marshal(claims)
		if err != nil {
			t.Fatal(err)
		}
		return "e30." + base64.RawURLEncoding.EncodeToString(raw) + ".fixture"
	}
	writeJSON(t, filepath.Join(codexDir, "auth.json"), map[string]any{
		"auth_mode": "chatgpt",
		"tokens": map[string]any{
			"id_token":      jwt(map[string]any{"email": "bundle@example.com", "https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": "team", "chatgpt_account_id": workspace}}),
			"access_token":  jwt(map[string]any{"exp": time.Now().Add(time.Hour).Unix()}),
			"refresh_token": "fixture-refresh-" + workspace,
			"account_id":    workspace,
		},
	})
	writeJSON(t, filepath.Join(codexDir, "models_cache.json"), map[string]any{"models": []any{map[string]any{"slug": "gpt-5.5", "display_name": "GPT-5.5", "visibility": "list", "priority": 1}}})
	provider.ForgetAccounts()
	provider.Changed()
	// This public read discovers and persists the agent's auth.json normally.
	ls := provider.Logins("codex")
	if len(ls) != 1 {
		t.Fatalf("normal discovery did not find one Codex identity: %+v", ls)
	}
	saved := bundleCompatSaved(t)
	row, ok := saved[workspace]
	if !ok || row.ID == "" || row.ID != provider.LoginID("codex", ls[0].User) {
		t.Fatalf("normal saved workspace %s missing or inconsistent", workspace)
	}
	return row
}

func bundleCompatAliasRegistry(t *testing.T) map[string]json.RawMessage {
	t.Helper()
	ps, err := provider.Stored()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range ps {
		if p.ID != "codex" {
			continue
		}
		raw, err := json.Marshal(p)
		if err != nil {
			t.Fatal(err)
		}
		var row struct {
			Aliases map[string]json.RawMessage `json:"accountAliases"`
		}
		if err = json.Unmarshal(raw, &row); err != nil {
			t.Fatal(err)
		}
		return row.Aliases
	}
	return nil
}

func bundleCompatRoundTrip(t *testing.T, withAlias bool) (Bundle, bundleCompatLogin) {
	t.Helper()
	source := bundleCompatMachine(t, "workspace-b")
	p, err := provider.Find("codex")
	if err != nil {
		t.Fatal(err)
	}
	p.Models = []string{"gpt-5.5"}
	if err = provider.Save(*p); err != nil {
		t.Fatal(err)
	}
	if withAlias {
		if err = provider.SetAccountRoutingAlias("codex", source.ID, "work"); err != nil {
			t.Fatal(err)
		}
	}
	b, err := Collect(true, "bundle-owner-compat")
	if err != nil {
		t.Fatal(err)
	}
	if err = WithSignIns(&b); err != nil {
		t.Fatal(err)
	}
	if len(b.Providers) != 1 || b.SignIns == nil || len(b.SignIns.Logins) != 1 {
		t.Fatalf("full source collection did not carry provider and sign-in: providers=%d signIns=%+v", len(b.Providers), b.SignIns)
	}
	sealed, err := Seal(b, "fixture bundle compatibility passphrase")
	if err != nil {
		t.Fatal(err)
	}
	opened, err := Open(sealed, "fixture bundle compatibility passphrase")
	if err != nil {
		t.Fatal(err)
	}
	return opened, source
}

func bundleCompatRestore(t *testing.T, withAlias bool) {
	t.Helper()
	b, source := bundleCompatRoundTrip(t, withAlias)
	original := bundleCompatMachine(t, "workspace-a")
	if source.ID != original.ID || source.User != original.User {
		t.Fatal("normal independent Team saves must generate the same name and ID for this fixture")
	}
	beforeAliases := bundleCompatAliasRegistry(t)
	if len(beforeAliases) != 0 {
		t.Fatal("target already has alias history; source bundle must introduce it")
	}
	result, restoreErr := Restore(b, Parts{Providers: true, SignIns: true})
	t.Logf("full bundle restore: withAlias=%v source B ID=%s target A ID=%s result=%+v signIns=%+v error=%v", withAlias, source.ID, original.ID, result, result.SignIns, restoreErr)
	after := bundleCompatSaved(t)
	a, haveA := after["workspace-a"]
	if !haveA || a.ID != original.ID || !reflect.DeepEqual(a.Auth, original.Auth) {
		t.Error("full bundle restore changed original workspace A identity or credential")
	}
	incoming, haveB := after["workspace-b"]
	if !withAlias {
		if restoreErr != nil || !haveB || result.SignIns == nil || result.SignIns.Added != 1 {
			t.Fatalf("legal no-alias full bundle import failed: B=%v result=%+v error=%v", haveB, result.SignIns, restoreErr)
		}
		if !reflect.DeepEqual(incoming.Auth, source.Auth) {
			t.Error("legal no-alias full bundle import changed workspace B credential")
		}
		t.Log("legal no-alias full bundle imported B alongside A; generic ID uniqueness is not asserted")
		return
	}
	registry := bundleCompatAliasRegistry(t)
	qualified, _, qualifiedOK := provider.Resolve("codex/gpt-5.5@work")
	allowsA, allowsB := false, false
	if qualifiedOK {
		allowsA = qualified.RoutingAllows(a.ID)
		if haveB {
			allowsB = qualified.RoutingAllows(incoming.ID)
		}
	}
	t.Logf("final routing: workResolved=%v A=%v A.id=%s B=%v B.id=%s workAllowsA=%v workAllowsB=%v registry=%s", qualifiedOK, haveA, a.ID, haveB, incoming.ID, allowsA, allowsB, registry["work"])
	// Inspect routing even after an error: a late rejection must not leave the
	// incoming alias attached to the target's unrelated original workspace.
	if allowsA {
		t.Error("incoming bundle's work alias permits original workspace A instead of being confined to source workspace B")
	}
	if restoreErr != nil {
		if !reflect.DeepEqual(registry, beforeAliases) {
			t.Error("rejected bundle introduced alias history on the original owner")
		}
		return
	}
	if !haveB || !reflect.DeepEqual(incoming.Auth, source.Auth) {
		t.Error("successful aliased bundle import did not preserve workspace B credential")
	}
	if !qualifiedOK || !allowsB {
		t.Error("successful aliased bundle import does not permit source workspace B through work")
	}
	if haveA && haveB && a.ID == incoming.ID {
		t.Error("successful full bundle import made A and B share the qualified owner")
	}
}

func TestRoutingAliasSignInsCompatBundleNoAlias(t *testing.T) {
	bundleCompatRestore(t, false)
}

func TestRoutingAliasSignInsCompatBundleIncomingOwner(t *testing.T) {
	bundleCompatRestore(t, true)
}

// The target has already saved this AuthWho under a different initial label.
// Reservation arrives only in the sealed source bundle, after sign-in apply.
func TestRoutingAliasSignInsCompatBundlePluginIncomingOwner(t *testing.T) {
	old := addPlugins
	t.Cleanup(func() { addPlugins = old })
	addPlugins = func(context.Context, []plugin.Entry) ([]string, map[string]error) {
		return nil, nil // synthetic plugins need no installation for the restore checks
	}
	t.Cleanup(func() { plugin.UseCached(nil) })
	const spec = "@example/opencode-acme-auth"
	publish := func(label, token string, expires int64) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(provider.Path()), 0700); err != nil {
			t.Fatal(err)
		}
		writeJSON(t, filepath.Join(filepath.Dir(provider.Path()), "plugins.json"), map[string]any{"plugins": []any{map[string]any{"spec": spec}}})
		writeJSON(t, plugin.AuthPath(), map[string]any{"acme": map[string]any{"type": "oauth", "accountId": label, "access": token, "refresh": "refresh-" + token, "expires": expires}})
		plugin.UseCached([]plugin.Provider{{ID: "acme", Spec: spec, Name: "Acme", Methods: []plugin.Method{{Type: "oauth", Label: "Sign in"}}, Models: []plugin.Model{{ID: "m", Name: "Model"}}, Accounts: []plugin.Account{{Key: "acme", Type: "oauth", AccountID: label, Models: []string{"m"}}}}})
		provider.Changed()
		if ls := provider.Logins("plugin:acme"); len(ls) != 1 {
			t.Fatalf("normal plugin discovery found %d accounts", len(ls))
		}
	}
	entries := func() map[string]json.RawMessage {
		t.Helper()
		auth, err := plugin.AuthEntries()
		if err != nil {
			t.Fatal(err)
		}
		return auth
	}
	home(t)
	publish("source-first@fake", "new", 5000000000000)
	sourceOwner := provider.LoginID("plugin:acme", "source-first@fake")
	publish("same@fake", "new", 5000000000000)
	if got := provider.LoginID("plugin:acme", "same@fake"); got != sourceOwner {
		t.Fatal("source label change did not preserve saved owner")
	}
	if err := provider.SetAccountRoutingAlias("acme", sourceOwner, "work"); err != nil {
		t.Fatal(err)
	}
	b, err := Collect(true, "plugin-bundle-owner-compat")
	if err != nil {
		t.Fatal(err)
	}
	if err = WithSignIns(&b); err != nil {
		t.Fatal(err)
	}
	if len(b.Providers) != 1 || b.SignIns == nil || len(b.SignIns.Plugins) != 1 {
		t.Fatal("source full bundle omitted provider or plugin credential")
	}
	sealed, err := Seal(b, "fixture plugin bundle passphrase")
	if err != nil {
		t.Fatal(err)
	}
	opened, err := Open(sealed, "fixture plugin bundle passphrase")
	if err != nil {
		t.Fatal(err)
	}

	plugin.UseCached(nil)
	home(t)
	publish("target-first@fake", "old", 4000000000000)
	targetOwner := provider.LoginID("plugin:acme", "target-first@fake")
	publish("same@fake", "old", 4000000000000)
	if targetOwner == sourceOwner {
		t.Fatal("normal source and target discovery did not generate different owners")
	}
	ps, err := provider.Stored()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range ps {
		if p.HasRoutingAliases() {
			t.Fatal("target has alias history before full bundle restore")
		}
	}
	originalAuth := entries()
	originalLogins, err := os.ReadFile(filepath.Join(filepath.Dir(provider.Path()), "logins.json"))
	if err != nil {
		t.Fatal(err)
	}
	result, restoreErr := Restore(opened, Parts{Providers: true, SignIns: true})
	auth := entries()
	finalOwner := provider.LoginID("plugin:acme", "same@fake")
	qualified, _, workOK := provider.Resolve("acme/m@work")
	ordinary, _, ordinaryOK := provider.Resolve("acme/m")
	allowsOrdinary := ordinaryOK && ordinary.RoutingAllows(finalOwner)
	t.Logf("sealed plugin restore: error=%v signIns=%+v source=%s originalTarget=%s final=%s workResolved=%v ordinaryAllowsImported=%v", restoreErr, result.SignIns, sourceOwner, targetOwner, finalOwner, workOK, allowsOrdinary)
	if restoreErr != nil {
		now, err := os.ReadFile(filepath.Join(filepath.Dir(provider.Path()), "logins.json"))
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(auth, originalAuth) || string(now) != string(originalLogins) {
			t.Error("rejected plugin bundle changed original credential or saved identity")
		}
		ps, err := provider.Stored()
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range ps {
			if p.HasRoutingAliases() {
				t.Error("rejected plugin bundle left incoming alias history on target")
			}
		}
		return
	}
	var actual, expected any
	if err = json.Unmarshal(auth["acme"], &actual); err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(opened.SignIns.Plugins["acme"], &expected); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(actual, expected) {
		t.Error("successful plugin restore did not import newer source credential")
	}
	if finalOwner != sourceOwner {
		t.Error("source alias reservation arrived after import retained a different target owner")
	}
	if !workOK || qualified.Account == nil || qualified.RoutingID() != sourceOwner {
		t.Error("source work alias cannot select restored plugin credential")
	}
	if allowsOrdinary {
		t.Error("newer credential reserved by incoming work alias was released to ordinary routing")
	}
}
