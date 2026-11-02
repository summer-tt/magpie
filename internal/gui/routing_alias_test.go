package gui

import (
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

	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/provider"
)

// Exercise the actual UI API -> persisted provider -> catalog -> guarded gateway.
func TestRoutingAliasAPIToGateway(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	var mu sync.Mutex
	var calls []string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/models") {
			io.WriteString(w, `{"data":[{"id":"m"}]}`)
			return
		}
		var b struct {
			Model string `json:"model"`
		}
		json.NewDecoder(r.Body).Decode(&b)
		mu.Lock()
		calls = append(calls, r.Header.Get("Authorization")+":"+b.Model)
		mu.Unlock()
		io.WriteString(w, `{"id":"answer","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}]}`)
	}))
	defer up.Close()
	p := provider.Provider{ID: "alias-api", Name: "Alias API", Chat: up.URL + "/v1", Key: "key-first", Keys: []provider.KeyAccount{{Key: "key-work"}}, Models: []string{"m"}}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	gw := gateway.New().Front()
	send := func(model string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "http://localhost/v1/chat/completions", strings.NewReader(`{"model":"`+model+`","messages":[{"role":"user","content":"hello"}]}`))
		r.RemoteAddr = "127.0.0.1:12345"
		w := httptest.NewRecorder()
		gw.ServeHTTP(w, r)
		return w
	}
	if w := send("alias-api/m"); w.Code != 200 {
		t.Fatalf("baseline ordinary route: %d %s", w.Code, w.Body.String())
	}
	mu.Lock()
	baseline := slices.Clone(calls)
	calls = nil
	mu.Unlock()
	if !slices.Equal(baseline, []string{"Bearer key-first:m"}) {
		t.Fatalf("baseline calls = %v", baseline)
	}
	api := Handler(nil, nil)
	post := func(alias, ref string) *httptest.ResponseRecorder {
		b, _ := json.Marshal(map[string]string{"id": p.ID, "ref": ref, "alias": alias})
		w := httptest.NewRecorder()
		api.ServeHTTP(w, httptest.NewRequest("POST", "/api/keys/alias", strings.NewReader(string(b))))
		return w
	}
	ref := provider.KeyID("key-work")
	w := post(" Work ", ref)
	if w.Code != 200 {
		t.Fatalf("routing alias setting API: status %d: %s; want 200", w.Code, w.Body.String())
	}
	data, err := os.ReadFile(provider.Path())
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Providers []struct {
			ID         string `json:"id"`
			Key        string `json:"key"`
			KeyAliases map[string]struct {
				OwnerID string `json:"ownerId"`
				KeyID   string `json:"keyId"`
				Active  bool   `json:"active"`
			} `json:"keyAliases"`
		}
	}
	if err = json.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, row := range cfg.Providers {
		if row.ID != p.ID {
			continue
		}
		found = true
		b := row.KeyAliases["work"]
		if !b.Active || b.KeyID != ref || b.OwnerID != ref || row.Key != "key-first" {
			t.Errorf("saved alias/key identity = %+v", row)
		}
	}
	if !found {
		t.Fatal("provider disappeared")
	}
	listed := false
	for _, e := range provider.Catalog() {
		if e.ID == "alias-api/m@work" {
			listed = true
		}
	}
	if !listed {
		t.Error("API save did not publish qualified catalog entry")
	}
	if w := send("alias-api/m@work"); w.Code != 200 {
		t.Errorf("qualified route: %d %s", w.Code, w.Body.String())
	}
	mu.Lock()
	got := slices.Clone(calls)
	calls = nil
	mu.Unlock()
	if !slices.Equal(got, []string{"Bearer key-work:m"}) {
		t.Errorf("API configured qualified upstream = %v", got)
	}
	// Clearing retires the spelling; another owner cannot acquire the old name.
	if w := post("", ref); w.Code != 200 {
		t.Fatalf("clear: %d %s", w.Code, w.Body.String())
	}
	if w := send("alias-api/m@work"); w.Code != 404 {
		t.Errorf("cleared reference: %d %s", w.Code, w.Body.String())
	}
	if w := post("work", provider.KeyID("key-first")); w.Code < 400 {
		t.Errorf("retired alias transferred to different owner: %d %s", w.Code, w.Body.String())
	}
	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 0 {
		t.Errorf("cleared alias consumed upstream: %v", calls)
	}
}
