package gui

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/agent"
	"github.com/yetone/magpie/internal/provider"
)

// The copy entry's data (gui-shell "Editing account routing aliases"):
// /api/providers answers each aliased owner's complete qualified references
// under its stable owner ID alone, from the models that owner serves — the
// other owner's models stay out, and no default effort or window is
// attached to the reference. The state is read as raw JSON, so the same
// test is a behavior red where the field is missing, not a build break.
func TestRoutingAliasCopyModelsPerOwner(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	if err := provider.Save(provider.Provider{ID: "alias-copy", Name: "Alias copy",
		Chat: "http://127.0.0.1:1/v1", Key: "key-main", Keys: []provider.KeyAccount{{Key: "key-work"}},
		Models: []string{"m", "m2", "m3"}}); err != nil {
		t.Fatal(err)
	}
	main, work := provider.KeyID("key-main"), provider.KeyID("key-work")
	// each owner serves a different two of the provider's three models
	for owner, models := range map[string][]string{main: {"m", "m2"}, work: {"m2", "m3"}} {
		if err := provider.SetAccountModels("alias-copy", owner, models); err != nil {
			t.Fatal(err)
		}
	}
	h := Handler(nil, nil)
	post := func(ref, alias string) *httptest.ResponseRecorder {
		b, _ := json.Marshal(map[string]string{"id": "alias-copy", "ref": ref, "alias": alias})
		return copyModelsServe(h, httptest.NewRequest("POST", "/api/keys/alias", strings.NewReader(string(b))))
	}
	if w := post(main, "main"); w.Code != 200 {
		t.Fatalf("alias of the first key: %d %s", w.Code, w.Body.String())
	}
	if w := post(work, "work"); w.Code != 200 {
		t.Fatalf("alias of the second key: %d %s", w.Code, w.Body.String())
	}
	w := copyModelsServe(h, httptest.NewRequest("GET", "/api/providers", nil))
	var state struct {
		Providers []struct {
			ID            string              `json:"id"`
			RoutingModels map[string][]string `json:"routingModels"`
			KeyList       []struct {
				ID string `json:"id"`
			} `json:"keyList"`
		} `json:"providers"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	var row *struct {
		ID            string              `json:"id"`
		RoutingModels map[string][]string `json:"routingModels"`
		KeyList       []struct {
			ID string `json:"id"`
		} `json:"keyList"`
	}
	for i := range state.Providers {
		if state.Providers[i].ID == "alias-copy" {
			row = &state.Providers[i]
		}
	}
	if row == nil {
		t.Fatalf("provider missing from /api/providers: %s", w.Body.String())
	}
	// each owner's own complete references, in model order, and neither the
	// other owner's models nor a shorter list of its own
	if !slices.Equal(row.RoutingModels[main], []string{"alias-copy/m@main", "alias-copy/m2@main"}) {
		t.Errorf("first owner's routingModels = %v; want its own two references in model order", row.RoutingModels[main])
	}
	if !slices.Equal(row.RoutingModels[work], []string{"alias-copy/m2@work", "alias-copy/m3@work"}) {
		t.Errorf("second owner's routingModels = %v; want its own two, not the first owner's models", row.RoutingModels[work])
	}
	for _, refs := range row.RoutingModels {
		for _, ref := range refs {
			if strings.ContainsAny(ref, ":~[]") || strings.HasSuffix(ref, "1m]") {
				t.Errorf("reference %q has an effort or window the owner never set", ref)
			}
		}
	}
	// the owners are the stable IDs the key rows carry, and no other owner
	// is listed: a key serving nothing is simply absent, never filled from
	// another owner's list
	ids := make([]string, 0, len(row.KeyList))
	for _, k := range row.KeyList {
		ids = append(ids, k.ID)
	}
	owners := make([]string, 0, len(row.RoutingModels))
	for owner := range row.RoutingModels {
		owners = append(owners, owner)
	}
	slices.Sort(owners)
	slices.Sort(ids)
	if !slices.Equal(owners, ids) {
		t.Errorf("routingModels owners = %v; the key rows' ids = %v", owners, ids)
	}
}

// The same copy data for subscription accounts, through the real
// discovery path rather than the page's mocked shape (gui-shell "Editing
// account routing aliases"): Codex signed in under a sandboxed home, a
// second ChatGPT account saved beside it and on, each account's models
// list answered by a loopback ChatGPT backend per its own sign-in. The
// active account is further held to one model by its own account-model
// list; the saved one serves its whole plan. Each account's alias set
// through the account API, /api/providers answers each owner's complete
// references under the stable id its account row carries.
func TestRoutingAliasCopyModelsSubscriptionOwners(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	claim := func(v any) string {
		b, _ := json.Marshal(v)
		return "h." + base64.RawURLEncoding.EncodeToString(b) + ".s"
	}
	// each account's own sign-in: its access token far from expiry, so no
	// refresh is asked, and its email in the id token
	tokSeat, tokTeam := claim(map[string]any{"exp": 9e12, "sub": "seat@example.com"}), claim(map[string]any{"exp": 9e12, "sub": "team@example.com"})
	signedIn := func(email, accountID, token string) map[string]any {
		return map[string]any{"auth_mode": "chatgpt", "tokens": map[string]any{
			"id_token":     claim(map[string]any{"email": email, "https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": "plus", "chatgpt_account_id": accountID}}),
			"access_token": token, "refresh_token": "refresh-" + accountID, "account_id": accountID}}
	}
	list := func(ids ...string) string {
		ms := make([]map[string]any, len(ids))
		for i, id := range ids {
			ms[i] = map[string]any{"slug": id, "display_name": id, "visibility": "list", "priority": i + 1}
		}
		b, _ := json.Marshal(map[string]any{"models": ms})
		return string(b)
	}
	var mu sync.Mutex
	asked := map[string]int{}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/models") {
			w.WriteHeader(404)
			return
		}
		tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		mu.Lock()
		asked[tok]++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		// each account's plan has its own two of the provider's three models
		switch tok {
		case tokSeat:
			io.WriteString(w, list("gpt-5.5", "gpt-5.5-codex"))
		case tokTeam:
			io.WriteString(w, list("gpt-5.5-codex", "gpt-6-luna"))
		default:
			w.WriteHeader(404)
		}
	}))
	oldBase := provider.CodexBase
	provider.CodexBase = up.URL + "/backend-api/codex"
	provider.ForgetAccounts()
	t.Cleanup(func() {
		agent.SyncCatalogSettled()
		provider.CodexBase = oldBase
		provider.ForgetAccounts()
		up.Close()
	})
	if err := os.MkdirAll(filepath.Join(home, ".codex"), 0700); err != nil {
		t.Fatal(err)
	}
	auth, _ := json.Marshal(signedIn("seat@example.com", "fixture-seat", tokSeat))
	if err := os.WriteFile(filepath.Join(home, ".codex", "auth.json"), auth, 0600); err != nil {
		t.Fatal(err)
	}
	// the second account: a saved sign-in, on beside the agent's own
	teamAuth, _ := json.Marshal(signedIn("team@example.com", "fixture-team", tokTeam))
	logins, _ := json.Marshal([]map[string]any{{"agent": "codex", "user": "team@example.com", "plan": "Team", "on": true,
		"seen": time.Now().UTC().Format(time.RFC3339), "auth": json.RawMessage(teamAuth)}})
	if err := os.WriteFile(filepath.Join(filepath.Dir(provider.Path()), "logins.json"), logins, 0600); err != nil {
		t.Fatal(err)
	}
	p, err := provider.Find("codex")
	if err != nil || p.Account == nil || p.Account.User != "seat@example.com" {
		t.Fatalf("baseline subscription discovery: err=%v account=%+v", err, p.Account)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	ms, err := p.Fetch(ctx)
	if err != nil {
		t.Fatalf("baseline model lists: %v", err)
	}
	have := map[string]bool{}
	for _, m := range ms {
		have[m.ID] = true
	}
	for _, id := range []string{"gpt-5.5", "gpt-5.5-codex", "gpt-6-luna"} {
		if !have[id] {
			t.Fatalf("baseline union list lacks %s: %v", id, ms)
		}
	}
	mu.Lock()
	for _, tok := range []string{tokSeat, tokTeam} {
		if asked[tok] == 0 {
			t.Fatalf("the account of %s was never asked for its own list", tok)
		}
	}
	mu.Unlock()
	// the active account keeps only one of the two models its plan lists;
	// the saved account has no list of its own and serves its whole plan
	if err := provider.SetAccountModels("codex", "seat@example.com", []string{"gpt-5.5"}); err != nil {
		t.Fatal(err)
	}
	work := provider.LoginID("codex", "seat@example.com")
	team := provider.LoginID("codex", "team@example.com")
	h := Handler(nil, nil)
	post := func(account, alias string) *httptest.ResponseRecorder {
		b, _ := json.Marshal(map[string]string{"id": "codex", "account": account, "alias": alias})
		return copyModelsServe(h, httptest.NewRequest("POST", "/api/provider/accountalias", strings.NewReader(string(b))))
	}
	if w := post(work, "work"); w.Code != 200 {
		t.Fatalf("alias of the signed-in account: %d %s", w.Code, w.Body.String())
	}
	if w := post(team, "team"); w.Code != 200 {
		t.Fatalf("alias of the saved account: %d %s", w.Code, w.Body.String())
	}
	w := copyModelsServe(h, httptest.NewRequest("GET", "/api/providers", nil))
	var state struct {
		Providers []struct {
			ID            string              `json:"id"`
			RoutingModels map[string][]string `json:"routingModels"`
			Account       *struct {
				ID     string `json:"id"`
				Logins []struct {
					ID   string `json:"id"`
					User string `json:"user"`
				} `json:"logins"`
			} `json:"account"`
		} `json:"providers"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	var row *struct {
		ID            string              `json:"id"`
		RoutingModels map[string][]string `json:"routingModels"`
		Account       *struct {
			ID     string `json:"id"`
			Logins []struct {
				ID   string `json:"id"`
				User string `json:"user"`
			} `json:"logins"`
		} `json:"account"`
	}
	for i := range state.Providers {
		if state.Providers[i].ID == "codex" {
			row = &state.Providers[i]
		}
	}
	if row == nil || row.Account == nil {
		t.Fatalf("codex missing from /api/providers: %s", w.Body.String())
	}
	if len(row.Account.Logins) != 2 {
		t.Errorf("account rows = %v; want the signed-in and the saved account", row.Account.Logins)
	}
	// each owner's own references: the held account its one allowed model
	// (not the other its plan lists), the saved account its whole plan
	want := map[string][]string{
		work: {"codex/gpt-5.5@work"},
		team: {"codex/gpt-5.5-codex@team", "codex/gpt-6-luna@team"},
	}
	for owner, refs := range row.RoutingModels {
		got := slices.Clone(refs)
		slices.Sort(got)
		expected := slices.Clone(want[owner])
		slices.Sort(expected)
		if !slices.Equal(got, expected) {
			t.Errorf("owner %s routingModels = %v; want %v", owner, got, expected)
		}
	}
	if len(row.RoutingModels) != len(want) {
		t.Errorf("routingModels owners = %d %v; want exactly the two accounts", len(row.RoutingModels), row.RoutingModels)
	}
	for _, refs := range row.RoutingModels {
		for _, ref := range refs {
			if strings.ContainsAny(ref, ":~[]") || strings.HasSuffix(ref, "1m]") {
				t.Errorf("reference %q has an effort or window the account never set", ref)
			}
		}
	}
	// the owners are the stable ids the account rows themselves carry: the
	// account's own and its logins', one id however many ways it is listed
	rowIDs := map[string]bool{row.Account.ID: true}
	for _, l := range row.Account.Logins {
		rowIDs[l.ID] = true
	}
	owners := make([]string, 0, len(row.RoutingModels))
	for owner := range row.RoutingModels {
		owners = append(owners, owner)
	}
	slices.Sort(owners)
	rows := make([]string, 0, len(rowIDs))
	for id := range rowIDs {
		rows = append(rows, id)
	}
	slices.Sort(rows)
	if !slices.Equal(owners, rows) {
		t.Errorf("routingModels owners = %v; the account rows' ids = %v", owners, rows)
	}
}

func copyModelsServe(h http.Handler, r *http.Request) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
