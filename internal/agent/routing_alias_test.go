package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/filememo"
	"github.com/yetone/magpie/internal/provider"
)

func routingAliasAgentRegistry(t *testing.T, active bool) {
	t.Helper()
	b, err := os.ReadFile(provider.Path())
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err = json.Unmarshal(b, &cfg); err != nil {
		t.Fatal(err)
	}
	for _, raw := range cfg["providers"].([]any) {
		p := raw.(map[string]any)
		if p["id"] == "relay" {
			p["keyAliases"] = map[string]any{"work": map[string]any{"ownerId": provider.KeyID("k"), "keyId": provider.KeyID("k"), "active": active}}
		}
	}
	b, err = json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(provider.Path(), b, 0600); err != nil {
		t.Fatal(err)
	}
	filememo.Forget()
	catalog.Touched()
}

func TestRoutingAliasAgentConfigRoundTripAndOldPick(t *testing.T) {
	for _, kind := range []string{"codex", "claude", "pi", "minimax"} {
		t.Run(kind, func(t *testing.T) {
			home := syncHome(t)
			p, err := provider.Find("relay")
			if err != nil {
				t.Fatal(err)
			}
			p.Models = []string{"glm-4.6", "claude-opus-4.6"}
			if err = provider.Save(*p); err != nil {
				t.Fatal(err)
			}
			var a *Agent
			var base, qualified string
			var files []string
			switch kind {
			case "codex":
				writeFile(t, filepath.Join(home, ".codex", "config.toml"), "model = \"gpt-5.5\"\n")
				a = codex(home)
				base = "relay/glm-4.6"
				qualified = base + "@work"
				files = []string{filepath.Join(home, ".codex", "config.toml"), filepath.Join(home, ".codex", "magpie-models.json")}
			case "claude":
				writeFile(t, filepath.Join(home, ".claude", "settings.json"), `{"theme":"dark"}`)
				a = claude(home)
				base = "relay/claude-opus-4.6"
				qualified = base + "@work"
				files = []string{filepath.Join(home, ".claude", "settings.json")}
			case "pi":
				a = pi(home)
				base = "magpie/relay/glm-4.6"
				qualified = base + "@work"
				files = []string{filepath.Join(home, ".pi", "agent", "settings.json"), filepath.Join(home, ".pi", "agent", "models.json")}
			case "minimax":
				writeFile(t, filepath.Join(home, ".minimax", "config.yaml"), miniMaxConfig)
				a = miniMax(home)
				base = "magpie/relay/glm-4.6"
				qualified = base + "@work"
				files = []string{filepath.Join(home, ".minimax", "config.yaml")}
			}
			f := a.Field("model")
			if err := f.Set(base); err != nil {
				t.Fatalf("baseline %s config set: %v", kind, err)
			}
			if got := f.Get(); got != base {
				t.Fatalf("baseline %s config round-trip=%q want=%q", kind, got, base)
			}
			routingAliasAgentRegistry(t, true)
			if err := f.Set(qualified); err != nil {
				t.Fatalf("qualified %s config set: %v", kind, err)
			}
			if got := f.Get(); got != qualified {
				t.Errorf("qualified %s config round-trip=%q want=%q", kind, got, qualified)
			}
			combined := ""
			for _, path := range files {
				combined += readFile(path)
			}
			if !strings.Contains(combined, "@work") {
				t.Errorf("%s actual agent files lost alias", kind)
			}
			if kind == "minimax" {
				cfg, _ := readMiniMax(t, files[0])
				if cfg.DefaultModel != "custom_provider:magpie/relay~glm-4.6@work" {
					t.Errorf("MiniMax actual defaultModel=%q", cfg.DefaultModel)
				}
				if _, ok := cfg.CustomProvider["magpie"].Models["relay~glm-4.6@work"]; !ok {
					t.Error("MiniMax picker catalog lacks complete qualified flat ID")
				}
			}
			// A disappearing alias may update lists, but must not replace this explicit
			// selected identity with a base model or another account.
			savedIdentity := f.Get()
			routingAliasAgentRegistry(t, false)
			SyncCatalog()
			gotIdentity := f.Get()
			// Claude may expose its already-stored dashed spelling after the
			// catalog entry disappears. Both spellings preserve the same alias.
			if kind == "claude" {
				savedIdentity = provider.ClaudeDashed(savedIdentity)
				gotIdentity = provider.ClaudeDashed(gotIdentity)
			}
			if gotIdentity != savedIdentity {
				t.Errorf("%s SyncCatalog changed retired pick identity=%q want=%q", kind, gotIdentity, savedIdentity)
			}
		})
	}
}
