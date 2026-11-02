package gateway

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/filememo"
	"github.com/yetone/magpie/internal/proc"
	"github.com/yetone/magpie/internal/provider"
)

// Every assertion in child mode runs in a new OS process after TestMain.
// The parent's fake backend remains live, while no candidate/config cache is shared.
func TestRoutingAliasColdProcessConfigHealth(t *testing.T) {
	const modeEnv = "MAGPIE_ALIAS_COLD_MODE"
	if mode := os.Getenv(modeEnv); mode != "" {
		var env map[string]string
		if err := json.Unmarshal([]byte(os.Getenv("MAGPIE_ALIAS_COLD_ENV")), &env); err != nil {
			t.Fatal(err)
		}
		for k, v := range env {
			t.Setenv(k, v)
		}
		provider.CodexBase = os.Getenv("MAGPIE_ALIAS_COLD_BASE")
		r := httptest.NewRequest("POST", "http://localhost/v1/responses", strings.NewReader(`{"model":"codex/gpt-daybreak-blue-latest","input":"hello","stream":true}`))
		r.RemoteAddr = "127.0.0.1:1234"
		w := httptest.NewRecorder()
		New().Front().ServeHTTP(w, r)
		if mode == "healthy" {
			if w.Code != 200 {
				t.Fatalf("cold legal subscription: %d %s", w.Code, w.Body.String())
			}
		} else if w.Code < 400 {
			t.Errorf("cold %s served despite unknown current config: status=%d", mode, w.Code)
		}
		return
	}
	for _, mode := range []string{"malformed", "old-backup", "directory"} {
		t.Run(mode, func(t *testing.T) {
			codexSignedIn(t, "spare@example.com")
			d := daybreak(t)
			putProvider(t, map[string]any{"id": "codex", "models": []string{"gpt-daybreak-blue-latest"}, "accountAliases": map[string]any{"work": map[string]any{"accountId": provider.LoginID("codex", "spare@example.com"), "active": true}}})
			env := map[string]string{}
			for _, key := range []string{"HOME", "USERPROFILE", "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_DATA_HOME"} {
				env[key] = os.Getenv(key)
			}
			raw, _ := json.Marshal(env)
			run := func(mode string) {
				t.Helper()
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
				defer cancel()
				cmd := proc.CommandContext(ctx, os.Args[0], "-test.run=^TestRoutingAliasColdProcessConfigHealth$", "-test.timeout=90s")
				cmd.Env = append(os.Environ(), modeEnv+"="+mode, "MAGPIE_ALIAS_COLD_ENV="+string(raw), "MAGPIE_ALIAS_COLD_BASE="+provider.CodexBase)
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Errorf("fresh process %s: %v\n%s", mode, err, out)
				}
			}
			run("healthy")
			if d.asked() == 0 {
				t.Fatal("healthy cold subprocess never reached fake backend")
			}
			before := d.asked()
			path := provider.Path()
			if mode == "directory" {
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			} else {
				if mode == "old-backup" {
					if err := os.WriteFile(path+".bak", []byte(`{"providers":[{"id":"codex"}]}`), 0600); err != nil {
						t.Fatal(err)
					}
				}
				if err := os.WriteFile(path, []byte(`{"providers":[`), 0600); err != nil {
					t.Fatal(err)
				}
			}
			filememo.Forget()
			catalog.Touched()
			run(mode)
			if got := d.asked(); got != before {
				t.Errorf("cold unknown config consumed backend: before=%d after=%d", before, got)
			}
			// Ensure the subprocess used the same saved credential, not a newly invented account.
			if _, err := os.Stat(filepath.Join(env["HOME"], ".codex", "auth.json")); err != nil {
				t.Fatal(err)
			}
		})
	}
}
