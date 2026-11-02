package agent

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/filememo"
	"github.com/yetone/magpie/internal/provider"
)

func TestRoutingAliasCodexSubscriptionPickStaysQualified(t *testing.T) {
	claim := func(v any) string {
		b, _ := json.Marshal(v)
		return "h." + base64.RawURLEncoding.EncodeToString(b) + ".s"
	}
	auth, _ := json.Marshal(map[string]any{"tokens": map[string]any{"id_token": claim(map[string]any{"email": "seat@example.com"}), "access_token": claim(map[string]any{"exp": 9e12}), "refresh_token": "fixture-refresh", "account_id": "fixture-seat"}})
	home, read := codexHome(t, string(auth), "model = \"gpt-5.5\"\n")
	writeFile(t, filepath.Join(home, ".codex", "models_cache.json"), `{"models":[{"slug":"gpt-5.5","display_name":"GPT","visibility":"list"}]}`)
	provider.ForgetAccounts()
	t.Cleanup(provider.ForgetAccounts)
	a := codex(home)
	if err := a.Field("model").Set("codex/gpt-5.5"); err != nil {
		t.Fatalf("baseline native Codex pick: %v", err)
	}
	p, err := provider.Find("codex")
	if err != nil || p.Account == nil {
		t.Fatalf("baseline Codex subscription: %v", err)
	}
	raw, _ := json.Marshal(map[string]any{"providers": []any{map[string]any{"id": "codex", "accountAliases": map[string]any{"work": map[string]any{"accountId": provider.LoginID("codex", p.Account.User), "active": true}}}}})
	if err = os.WriteFile(provider.Path(), raw, 0600); err != nil {
		t.Fatal(err)
	}
	filememo.Forget()
	catalog.Touched()
	const ref = "codex/gpt-5.5@work"
	if err := a.Field("model").Set(ref); err != nil {
		t.Fatalf("qualified Codex pick: %v", err)
	}
	if got := a.Field("model").Get(); got != ref {
		t.Errorf("qualified Codex pick roundtrip=%q want=%q", got, ref)
	}
	if cfg := read(); !strings.Contains(cfg, "@work") || strings.Contains(cfg, "model = \"gpt-5.5\"") {
		t.Errorf("CodexOwn dropped routing identity in actual config: %s", cfg)
	}
}
