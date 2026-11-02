package gateway

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

func TestRoutingAliasDispatchRechecksQueuedCandidateAndKeepsSentStream(t *testing.T) {
	fresh(t)
	v := &slowVendor{release: map[string]chan struct{}{}, quit: make(chan struct{})}
	up := httptest.NewServer(v)
	one := 1
	if err := provider.Save(provider.Provider{ID: "alias-lane", Name: "Alias lane", Key: "lane-key", Chat: up.URL + "/v1", Models: []string{"m"}, MaxConcurrency: &one}); err != nil {
		up.Close()
		t.Fatal(err)
	}
	if err := provider.SetKeyRoutingAlias("alias-lane", provider.KeyID("lane-key"), "work"); err != nil {
		up.Close()
		t.Fatal(err)
	}
	s := New()
	gw := httptest.NewServer(s.Front())
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	type result struct {
		tag, body string
		code      int
		err       error
	}
	results := make(chan result, 3)
	t.Cleanup(func() { cancel(); close(v.quit); gw.Close(); up.Close(); wg.Wait() })
	send := func(tag string) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, _ := http.NewRequestWithContext(ctx, "POST", gw.URL+"/v1/chat/completions", strings.NewReader(`{"model":"alias-lane/m@work","stream":true,"messages":[{"role":"user","content":"`+tag+`"}]}`))
			r.Header.Set("Content-Type", "application/json")
			res, err := http.DefaultClient.Do(r)
			if err != nil {
				results <- result{tag: tag, err: err}
				return
			}
			b, err := io.ReadAll(res.Body)
			res.Body.Close()
			results <- result{tag: tag, body: string(b), code: res.StatusCode, err: err}
		}()
	}
	close(v.gate("baseline"))
	send("baseline")
	baseline := <-results
	if baseline.code != 200 || baseline.err != nil {
		t.Fatalf("baseline qualified lane: code=%d err=%v", baseline.code, baseline.err)
	}
	send("sent")
	within(t, "first stream reached upstream", func() bool { return len(v.seen()) == 2 })
	send("queued")
	laneID := "alias-lane#" + provider.KeyID("lane-key")
	within(t, "second candidate queued before send", func() bool { return s.Lanes()[laneID].Waiting == 1 })
	if err := provider.SetKeyRoutingAlias("alias-lane", provider.KeyID("lane-key"), "renamed"); err != nil {
		t.Fatal(err)
	}
	close(v.gate("sent"))
	for range 2 {
		r := <-results
		if r.tag == "sent" {
			if r.err != nil || r.code != 200 || !strings.Contains(r.body, "done") {
				t.Errorf("already sent stream interrupted: code=%d err=%v", r.code, r.err)
			}
		} else if r.err != nil || r.code < 400 {
			t.Errorf("queued stale alias status=%d err=%v; want local rejection", r.code, r.err)
		}
	}
	for _, tag := range v.seen() {
		if tag == "queued" {
			t.Error("queued candidate spent upstream after alias rename")
		}
	}
}
