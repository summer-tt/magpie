package gateway

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/testenv"
)

func TestRoutingAliasPluginSearchChecksCurrentAccount(t *testing.T) {
	bun := testenv.Bun(t)
	fresh(t)
	t.Setenv("MAGPIE_BUN", bun)
	t.Cleanup(plugin.Settle)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	abs, err := filepath.Abs("../plugin/testdata/searchauth")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := plugin.Add(ctx, abs); err != nil {
		t.Fatal(err)
	}
	if _, err := plugin.Providers(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := plugin.Import(ctx, "seekco", map[string]any{"type": "oauth", "access": "acc-b", "refresh": "r-b", "expires": time.Now().Add(time.Hour).UnixMilli(), "accountId": "b@seek"}); err != nil {
		t.Fatal(err)
	}
	var vendor provider.SearchVendor
	for _, v := range provider.AllSearchVendors() {
		if v.Plugin == abs && v.PluginID == "seek" {
			vendor = v
		}
	}
	if vendor.ID == "" {
		t.Fatal("search vendor missing")
	}
	_, before := provider.SearchSignIn(vendor)
	if before == nil {
		t.Fatal("signed-in provider missing")
	}
	// A newly discovered plugin can exist before providers.json is written.
	// Persist the ordinary configuration that the later backup must restore.
	if err := provider.SetAccountRoutingAlias(before.ID, before.RoutingID(), ""); err != nil {
		t.Fatal(err)
	}
	path := provider.Path()
	plain, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	was := runPluginSearchAs
	runPluginSearchAs = func(ctx context.Context, pkg, id, query string, limit int, account string) ([]plugin.SearchPage, error) {
		calls++
		return was(ctx, pkg, id, query, limit, account)
	}
	t.Cleanup(func() { runPluginSearchAs = was })
	s := New()
	a := provider.SearchAPI{Vendor: vendor.ID}
	if pages, err := s.askSearchSignedIn(ctx, a, vendor, "go"); err != nil || len(pages) != 1 || calls != 1 {
		t.Fatalf("ordinary search: pages=%+v calls=%d err=%v", pages, calls, err)
	}
	if err := provider.SetAccountRoutingAlias(before.ID, before.RoutingID(), "work"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.askSearchSignedIn(ctx, a, vendor, "go"); err == nil || calls != 1 {
		t.Fatalf("active alias: calls=%d err=%v", calls, err)
	}
	if _, err := s.askPluginSearchAs(ctx, vendor, *before, "go"); err == nil || calls != 1 {
		t.Errorf("account reserved after selection: calls=%d err=%v", calls, err)
	}
	if err := os.WriteFile(path+".bak", plain, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("!invalid current providers"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, restored := provider.SearchSignIn(vendor); restored == nil || len(accountsOn(*restored, "")) != 1 {
		t.Fatal("old backup must offer the ordinary account to exercise dispatch validation")
	}
	if _, err := s.askSearchSignedIn(ctx, a, vendor, "go"); err == nil || calls != 1 {
		t.Errorf("corrupt current configuration: calls=%d err=%v", calls, err)
	}
}
