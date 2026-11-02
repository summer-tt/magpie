package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

func TestRoutingAliasCodexNativeCompactExcludesAliasedAccount(t *testing.T) {
	codexSignedIn(t, "spare@example.com")
	if err := provider.SetLoginOn("codex", "me@example.com", false); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var accounts []string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		accounts = append(accounts, r.Header.Get("chatgpt-account-id"))
		mu.Unlock()
		if r.URL.Path != "/backend-api/codex/responses/compact" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"output":[{"type":"compaction","encrypted_content":"gAAAAAx"}]}`)
	}))
	t.Cleanup(up.Close)
	old := provider.CodexBase
	provider.CodexBase = up.URL + "/backend-api/codex"
	t.Cleanup(func() { provider.CodexBase = old })

	h := New().Front()
	bearer := codexAccessToken(t)
	compact := func() (int, string, []string) {
		t.Helper()
		r := httptest.NewRequest(http.MethodPost, "http://localhost"+CodexPath+"/responses/compact", strings.NewReader(`{"model":"gpt-5.5","input":[{"type":"message","role":"user","content":"ping"}]}`))
		r.RemoteAddr = "127.0.0.1:12345"
		r.Header.Set("Authorization", "Bearer "+bearer)
		r.Header.Set("chatgpt-account-id", "acct-1")
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		mu.Lock()
		got := slices.Clone(accounts)
		accounts = nil
		mu.Unlock()
		return w.Code, w.Body.String(), got
	}
	code, body, got := compact()
	if code != http.StatusOK || !strings.Contains(body, `"type":"compaction"`) || !slices.Equal(got, []string{"acct-2"}) {
		t.Fatalf("baseline compact: status=%d body=%s accounts=%v; want a successful compact signed by acct-2", code, body, got)
	}
	if err := provider.SetAccountRoutingAlias("codex", provider.LoginID("codex", "spare@example.com"), "work"); err != nil {
		t.Fatal(err)
	}
	code, body, got = compact()
	if code < http.StatusBadRequest || len(got) != 0 {
		t.Errorf("ordinary compact with only an aliased spare: status=%d body=%s accounts=%v; want an error and no upstream request", code, body, got)
	}
}
