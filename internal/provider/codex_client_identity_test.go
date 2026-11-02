package provider

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"
	"time"
)

// A Codex client may refresh its own credential in the keychain while
// magpie still holds the read before it. Its new token names the same
// account, so identity matching must read the changed item, without an
// OAuth refresh.
func TestCodexClientIdentityReadsRotatedKeychainToken(t *testing.T) {
	home := signIn(t)
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))
	k, account := codexInKeychain(t, home, "keyring")

	raw := k.item(account)
	var a codexAuth
	if err := json.Unmarshal(raw, &a); err != nil {
		t.Fatal(err)
	}
	t0 := a.Tokens.AccessToken
	if t0 == "" || codexAccountID(a) != "acct-1" {
		t.Fatalf("keychain sign-in: token %v, account %q", t0 != "", codexAccountID(a))
	}
	want := AccountRef{ID: LoginID("codex", "me@example.com"), User: "me@example.com"}
	ref, managed, err := CodexClientIdentity(t0, "acct-1")
	if err != nil || !managed || ref != want {
		t.Fatalf("the keychain sign-in: ref=%+v managed=%v err=%v; want %+v", ref, managed, err, want)
	}
	if finds, _ := k.counts(); finds != 1 {
		t.Fatalf("the first identity read asked the keychain %d times, want 1", finds)
	}

	// Codex refreshes the same account in the item; magpie's cached read
	// is still inside codexKeychainCheck.
	var stored map[string]any
	if err := json.Unmarshal(raw, &stored); err != nil {
		t.Fatal(err)
	}
	t1 := fakeJWT(map[string]any{"exp": float64(time.Now().Add(time.Hour).Unix()), "rotated": "by Codex"})
	stored["tokens"].(map[string]any)["access_token"] = t1
	next, err := json.Marshal(stored)
	if err != nil {
		t.Fatal(err)
	}
	k.set(account, next)

	var vendor []string
	was := http.DefaultClient.Transport
	http.DefaultClient.Transport = noVendor{&vendor}
	t.Cleanup(func() { http.DefaultClient.Transport = was })
	ref, managed, err = CodexClientIdentity(t1, "acct-1")
	if err != nil || !managed || ref != want {
		t.Fatalf("the refreshed keychain sign-in: ref=%+v managed=%v err=%v; want the same %+v", ref, managed, err, want)
	}
	if finds, _ := k.counts(); finds != 2 {
		t.Errorf("the refreshed identity was matched without reading the changed item: %d keychain reads", finds)
	}
	if len(vendor) != 0 {
		t.Errorf("identity matching asked the vendor: %v", vendor)
	}
}
