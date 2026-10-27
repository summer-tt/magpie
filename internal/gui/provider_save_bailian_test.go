package gui

// Bailian's Token Plan 个人版 is read with the Alibaba Cloud account's
// AccessKey ID and Secret (bailian_usage.go): the listing tells the editor
// that the Bailian Token Plan preset takes one and whose it is, and a
// provider of it its AccessKey ID and whether a Secret is saved, never the
// Secret. The Qwen AI platform's token plan takes none.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

func TestProviderListTellsBailianAccessKey(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	mux := http.NewServeMux()
	providerRoutes(mux, nil)

	p, err := provider.FromPreset("bailian-token-plan")
	if err != nil {
		t.Fatal(err)
	}
	p.Key, p.AccessKeyID, p.SecretAccessKey = "sk-sp-1", "LTAI-1", "SK-1"
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	q, err := provider.FromPreset("qwen-token-plan")
	if err != nil {
		t.Fatal(err)
	}
	q.ID, q.Name, q.Key = "qwen-token-plan", "Qwen Token Plan", "sk-sp-2"
	if err := provider.Save(q); err != nil {
		t.Fatal(err)
	}

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/api/providers", nil))
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if strings.Contains(w.Body.String(), "SK-1") {
		t.Error("the listing told the Secret")
	}
	var got struct {
		Providers []struct {
			ID        string         `json:"id"`
			AccessKey *accessKeyJSON `json:"accessKey"`
		} `json:"providers"`
		Presets []struct {
			ID              string `json:"id"`
			AccessKey       bool   `json:"accessKey"`
			AccessKeyVendor string `json:"accessKeyVendor"`
		} `json:"presets"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	ps := map[string]*accessKeyJSON{}
	for _, x := range got.Providers {
		ps[x.ID] = x.AccessKey
	}
	if a := ps["bailian-token-plan"]; a == nil || a.Vendor != "bailian" || a.ID != "LTAI-1" || !a.SecretSet {
		t.Errorf("bailian: %+v", a)
	}
	if a := ps["qwen-token-plan"]; a != nil {
		t.Errorf("the Qwen AI platform's plan takes one: %+v", a)
	}
	seen := 0
	for _, pr := range got.Presets {
		switch pr.ID {
		case "bailian-token-plan":
			seen++
			if !pr.AccessKey || pr.AccessKeyVendor != "bailian" {
				t.Errorf("bailian-token-plan preset: %+v", pr)
			}
		case "qwen-token-plan":
			seen++
			if pr.AccessKey || pr.AccessKeyVendor != "" {
				t.Errorf("qwen-token-plan preset: %+v", pr)
			}
		}
	}
	if seen != 2 {
		t.Errorf("presets seen: %d", seen)
	}
}
