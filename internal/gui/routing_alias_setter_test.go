package gui

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/agent"
	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/filememo"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/provider"
)

func aliasSetterHome(t *testing.T) {
	t.Helper()
	h := t.TempDir()
	for k, v := range map[string]string{"HOME": h, "USERPROFILE": h, "XDG_CONFIG_HOME": filepath.Join(h, "config"), "XDG_CACHE_HOME": filepath.Join(h, "cache"), "XDG_DATA_HOME": filepath.Join(h, "data")} {
		t.Setenv(k, v)
	}
}
func aliasSetterPost(h http.Handler, path string, body any) *httptest.ResponseRecorder {
	b, _ := json.Marshal(body)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("POST", path, bytes.NewReader(b)))
	return w
}
func aliasSetterKey(t *testing.T) (http.Handler, func(string, string, string) *httptest.ResponseRecorder) {
	t.Helper()
	aliasSetterHome(t)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/models") {
			io.WriteString(w, `{"data":[{"id":"m"}]}`)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"index": 0, "message": map[string]any{"role": "assistant", "content": r.Header.Get("Authorization")}, "finish_reason": "stop"}}})
	}))
	t.Cleanup(up.Close)
	for _, id := range []string{"setter", "other-setter"} {
		if err := provider.Save(provider.Provider{ID: id, Name: id, Chat: up.URL + "/v1", Key: "key-one", Keys: []provider.KeyAccount{{Key: "key-two"}}, Models: []string{"m"}}); err != nil {
			t.Fatal(err)
		}
	}
	r := httptest.NewRequest("POST", "http://localhost/v1/chat/completions", strings.NewReader(`{"model":"setter/m","messages":[{"role":"user","content":"hi"}]}`))
	r.RemoteAddr = "127.0.0.1:1234"
	w := httptest.NewRecorder()
	gateway.New().Front().ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("baseline setter gateway: %d %s", w.Code, w.Body.String())
	}
	h := Handler(nil, nil)
	return h, func(id, key, alias string) *httptest.ResponseRecorder {
		return aliasSetterPost(h, "/api/keys/alias", map[string]string{"id": id, "ref": provider.KeyID(key), "alias": alias})
	}
}
func TestRoutingAliasSetterGrammarAndOwnerHistory(t *testing.T) {
	for _, tc := range []struct {
		alias string
		valid bool
	}{{"a", true}, {strings.Repeat("a", 32), true}, {" Work._-1 ", true}, {strings.Repeat("a", 33), false}, {"1work", false}, {"_work", false}, {"work!", false}, {"w/x", false}, {"w@x", false}, {"w:x", false}, {"w~x", false}, {"w[x]", false}, {"w*x", false}} {
		t.Run(tc.alias, func(t *testing.T) {
			_, post := aliasSetterKey(t)
			before, _ := os.ReadFile(provider.Path())
			w := post("setter", "key-two", tc.alias)
			if tc.valid {
				if w.Code != 200 {
					t.Fatalf("legal setter: %d %s", w.Code, w.Body.String())
				}
				want := "setter/m@" + strings.ToLower(strings.TrimSpace(tc.alias))
				found := false
				for _, e := range provider.Catalog() {
					found = found || e.ID == want
				}
				if !found {
					t.Errorf("saved alias absent from catalog: %s", want)
				}
			} else {
				after, _ := os.ReadFile(provider.Path())
				if w.Code < 400 || !bytes.Equal(before, after) {
					t.Errorf("illegal alias status=%d modified=%v", w.Code, !bytes.Equal(before, after))
				}
			}
		})
	}
	_, post := aliasSetterKey(t)
	require := func(id, key, alias string, allowed bool) {
		t.Helper()
		before, _ := os.ReadFile(provider.Path())
		w := post(id, key, alias)
		after, _ := os.ReadFile(provider.Path())
		if allowed {
			if w.Code != 200 {
				t.Fatalf("setter %s/%s: %d %s", id, alias, w.Code, w.Body.String())
			}
		} else if w.Code < 400 || !bytes.Equal(before, after) {
			t.Errorf("owner conflict accepted/modified: status=%d changed=%v", w.Code, !bytes.Equal(before, after))
		}
	}
	require("setter", "key-two", "work", true)
	require("setter", "key-one", "work", false)
	require("other-setter", "key-one", "work", true)
	require("setter", "key-two", "renamed", true)
	if _, _, ok := provider.Resolve("setter/m@work"); ok {
		t.Error("renamed old alias still resolves")
	}
	require("setter", "key-one", "work", false)
	require("setter", "key-two", "work", true)
	require("setter", "key-two", "", true)
	require("setter", "key-one", "work", false)
	require("setter", "key-two", "work", true)
}
func TestRoutingAliasSetterRejectsDamagedCurrentWithOldBackup(t *testing.T) {
	_, post := aliasSetterKey(t)
	if w := post("setter", "key-two", "work"); w.Code != 200 {
		t.Fatalf("baseline alias setter=%d", w.Code)
	}
	path := provider.Path()
	old := []byte(`{"providers":[{"id":"setter","name":"Setter","chat":"http://127.0.0.1:1/v1","key":"key-one","keys":[{"key":"key-two"}],"models":["m"]}]}`)
	if err := os.WriteFile(path+".bak", old, 0600); err != nil {
		t.Fatal(err)
	}
	bad := []byte(`{"providers":[`)
	if err := os.WriteFile(path, bad, 0600); err != nil {
		t.Fatal(err)
	}
	filememo.Forget()
	catalog.Touched()
	w := post("setter", "key-two", "newname")
	after, _ := os.ReadFile(path)
	if w.Code < 400 {
		t.Errorf("setter accepted current unreadable config using old backup: status=%d", w.Code)
	}
	if !bytes.Equal(after, bad) {
		t.Error("setter overwrote damaged current config using stale backup")
	}
}
func TestRoutingAliasSetterConcurrentUniqueOwner(t *testing.T) {
	_, post := aliasSetterKey(t)
	start := make(chan struct{})
	codes := make(chan int, 2)
	var wg sync.WaitGroup
	for _, key := range []string{"key-one", "key-two"} {
		wg.Add(1)
		go func(key string) { defer wg.Done(); <-start; codes <- post("setter", key, "shared").Code }(key)
	}
	close(start)
	wg.Wait()
	close(codes)
	success := 0
	for code := range codes {
		if code == 200 {
			success++
		} else if code < 400 {
			t.Errorf("unexpected conflict status=%d", code)
		}
	}
	if success != 1 {
		t.Errorf("concurrent owner setters succeeded=%d want1", success)
	}
}
func TestRoutingAliasSubscriptionSetterKeepsCredentialsAndDisplayName(t *testing.T) {
	aliasSetterHome(t)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"models":[{"slug":"gpt-5.5","display_name":"GPT","visibility":"list"}]}`)
	}))
	oldBase := provider.CodexBase
	provider.CodexBase = up.URL + "/backend-api/codex"
	defer func() { agent.SyncCatalogSettled(); provider.CodexBase = oldBase; up.Close() }()
	home := os.Getenv("HOME")
	dir := filepath.Join(home, ".codex")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	claim := func(v any) string {
		b, _ := json.Marshal(v)
		return "h." + base64.RawURLEncoding.EncodeToString(b) + ".s"
	}
	auth, _ := json.Marshal(map[string]any{"auth_mode": "chatgpt", "tokens": map[string]any{"id_token": claim(map[string]any{"email": "seat@example.com"}), "access_token": claim(map[string]any{"exp": 9e12}), "refresh_token": "fixture-refresh", "account_id": "fixture-seat"}})
	authPath := filepath.Join(dir, "auth.json")
	if err := os.WriteFile(authPath, auth, 0600); err != nil {
		t.Fatal(err)
	}
	provider.ForgetAccounts()
	t.Cleanup(provider.ForgetAccounts)
	p, err := provider.Find("codex")
	if err != nil || p.Account == nil {
		t.Fatalf("baseline subscription discovery: %v", err)
	}
	owner := provider.LoginID("codex", p.Account.User)
	if err := provider.SetAccountName("codex", p.Account.User, "Display only"); err != nil {
		t.Fatal(err)
	}
	loginPath := filepath.Join(filepath.Dir(provider.Path()), "logins.json")
	loginBefore, _ := os.ReadFile(loginPath)
	authBefore, _ := os.ReadFile(authPath)
	h := Handler(nil, nil)
	w := aliasSetterPost(h, "/api/provider/accountalias", map[string]string{"id": "codex", "account": owner, "alias": "Work"})
	if w.Code != 200 {
		t.Fatalf("subscription alias setter: %d %s", w.Code, w.Body.String())
	}
	after, _ := os.ReadFile(authPath)
	loginsAfter, _ := os.ReadFile(loginPath)
	if !bytes.Equal(authBefore, after) || !bytes.Equal(loginBefore, loginsAfter) {
		t.Error("alias setter changed sign-in credentials/records")
	}
	p, err = provider.Find("codex")
	if err != nil {
		t.Fatal(err)
	}
	if p.AccountNames[strings.ToLower(p.Account.User)] != "Display only" {
		t.Error("routing alias changed account display name")
	}
	w = aliasSetterPost(h, "/api/provider/accountalias", map[string]string{"id": "codex", "account": "Display only", "alias": "other"})
	if w.Code < 400 {
		t.Error("routing alias API accepted display name instead of stable routing ID")
	}
}

func TestRoutingAliasSetterClearRestoresOrdinaryAndCrossRegistryRejects(t *testing.T) {
	_, post := aliasSetterKey(t)
	send := func(ref string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "http://localhost/v1/chat/completions", strings.NewReader(`{"model":"`+ref+`","messages":[{"role":"user","content":"hi"}]}`))
		r.RemoteAddr = "127.0.0.1:1234"
		w := httptest.NewRecorder()
		gateway.New().Front().ServeHTTP(w, r)
		return w
	}
	if w := post("setter", "key-one", "work"); w.Code != 200 {
		t.Fatalf("baseline setter=%d", w.Code)
	}
	if w := send("setter/m@work"); w.Code != 200 || !strings.Contains(w.Body.String(), "Bearer key-one") {
		t.Fatalf("baseline qualified target=%d %s", w.Code, w.Body.String())
	}
	if w := send("setter/m"); w.Code != 200 || !strings.Contains(w.Body.String(), "Bearer key-two") {
		t.Errorf("ordinary while isolated=%d %s", w.Code, w.Body.String())
	}
	if w := post("setter", "key-one", ""); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if w := send("setter/m"); w.Code != 200 || !strings.Contains(w.Body.String(), "Bearer key-one") {
		t.Errorf("clear did not restore original ordinary owner=%d %s", w.Code, w.Body.String())
	}
	if w := send("setter/m@work"); w.Code != 404 {
		t.Errorf("clear old reference=%d", w.Code)
	}
	raw, err := os.ReadFile(provider.Path())
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err = json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	for _, value := range cfg["providers"].([]any) {
		p := value.(map[string]any)
		if p["id"] == "setter" {
			p["accountAliases"] = map[string]any{"reserved": map[string]any{"accountId": "unresolved-saved-owner", "active": false}}
		}
	}
	raw, _ = json.Marshal(cfg)
	if err = os.WriteFile(provider.Path(), raw, 0600); err != nil {
		t.Fatal(err)
	}
	filememo.Forget()
	catalog.Touched()
	before, _ := os.ReadFile(provider.Path())
	w := post("setter", "key-two", "reserved")
	after, _ := os.ReadFile(provider.Path())
	if w.Code < 400 || !bytes.Equal(before, after) {
		t.Errorf("cross account/key registry collision status=%d changed=%v", w.Code, !bytes.Equal(before, after))
	}
}
