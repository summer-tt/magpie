package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

func TestRoutingAliasCodexBackendSearchAccounts(t *testing.T) {
	codexSignedIn(t, "spare@example.com")
	reply, err := os.ReadFile("testdata/codex_alpha_search.json")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var accounts []string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/alpha/search") {
			http.NotFound(w, r)
			return
		}
		mu.Lock()
		accounts = append(accounts, r.Header.Get("chatgpt-account-id"))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(reply)
	}))
	t.Cleanup(up.Close)
	old := provider.CodexBase
	provider.CodexBase = up.URL + "/backend-api/codex"
	t.Cleanup(func() { provider.CodexBase = old })
	if err := catalog.SaveLive("codex", provider.CodexBase, []catalog.Model{{ID: "gpt-6.1-sol"}, {ID: "gpt-6-luna"}}); err != nil {
		t.Fatal(err)
	}
	p, _, ok := provider.Resolve("codex/gpt-6-luna")
	if !ok {
		t.Fatal("resolve Codex search provider")
	}
	s := New()
	search := func(ctx context.Context, want string) {
		t.Helper()
		said, hits, err := s.codexSearch(ctx, p, "Go 1.26")
		mu.Lock()
		got := slices.Clone(accounts)
		accounts = nil
		mu.Unlock()
		if want == "" {
			if err == nil || len(got) != 0 {
				t.Errorf("search with no eligible account: said=%q hits=%v err=%v accounts=%v", said, hits, err, got)
			}
			return
		}
		if err != nil || len(hits) == 0 || !strings.Contains(said, "Green Tea garbage collector") || !slices.Equal(got, []string{want}) {
			t.Errorf("search: said=%q hits=%v err=%v accounts=%v; want successful search on %s", said, hits, err, got, want)
		}
	}
	// Establish that the real search fixture and signed first account work.
	search(context.Background(), "acct-1")
	if err := provider.SetAccountRoutingAlias("codex", provider.LoginID("codex", "me@example.com"), "work"); err != nil {
		t.Fatal(err)
	}
	search(context.Background(), "acct-2")
	for _, tc := range []struct {
		account string
		want    string
	}{
		{"codex/me@example.com", ""},
		{"codex/spare@example.com", "acct-2"},
	} {
		t.Run(tc.account, func(t *testing.T) {
			// A key's accounts are kept by their stable ids, as CleanAccounts resolves them.
			accounts, err := access.CleanAccounts([]string{tc.account})
			if err != nil {
				t.Fatal(err)
			}
			who := access.Identity{Models: access.HeldModels(nil, accounts), AccountModels: true, Accounts: accounts}
			ctx := access.WithIdentity(context.Background(), who)
			search(ctx, tc.want)
		})
	}
	// The original qualified request may not be rewritten to the ordinary pool.
	r := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	w := httptest.NewRecorder()
	r, ok = constrainRouting(w, r, provider.Responses, []byte(`{"model":"codex/gpt-6-luna@work"}`))
	if !ok {
		t.Fatalf("qualified context: %d %s", w.Code, w.Body.String())
	}
	search(r.Context(), "")
	if err := provider.SetAccountRoutingAlias("codex", provider.LoginID("codex", "spare@example.com"), "team"); err != nil {
		t.Fatal(err)
	}
	search(context.Background(), "")
}
