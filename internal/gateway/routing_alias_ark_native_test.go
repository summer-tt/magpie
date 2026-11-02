package gateway

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/filememo"
	"github.com/yetone/magpie/internal/provider"
)

// The native route against a primary key a routing alias reserves. Each
// test first proves the legal plain-key request on the same stack, then
// holds the behaviour the alias contract asks: a credential an active
// alias keeps for its qualified reference is not spent by an ordinary
// native generation, task list, task get or cancel — not when the alias
// was already set, not when the current providers file can't be read, and
// not when the alias is set while the request waits for its turn in the
// provider's minute.

// arkNativeRouting is one test's native route: the gateway server and the
// mux its own native handler hangs on, the caller every ask goes as, and
// the fake Ark native upstream the provider's key signs to.
type arkNativeRouting struct {
	h    http.Handler
	free access.Identity
	up   *arkNative
}

// standArkNative saves an Ark provider of one key pointing at the fake
// Ark native upstream, maxRPM its places in the minute (0: none set).
func standArkNative(t *testing.T, id, key string, maxRPM int) *arkNativeRouting {
	t.Helper()
	fresh(t)
	up := newArkNative(t)
	was := isArk
	isArk = func(p provider.Provider) bool { return p.ID == id }
	t.Cleanup(func() { isArk = was })
	if err := provider.Save(provider.Provider{ID: id, Name: "Volcengine Ark", Chat: up.URL + "/api/v3", Key: key, Models: []string{"doubao-seedream-4-5-251128"}, MaxRPM: maxRPM}); err != nil {
		t.Fatal(err)
	}
	return &arkNativeRouting{h: arkNativeMux(New()), free: access.Identity{KeyID: "desk", KeyName: "Desk"}, up: up}
}

func (f *arkNativeRouting) ask(t *testing.T, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	return arkNativeAsk(t, f.h, f.free, method, path, body)
}

// saw is what Ark has been asked so far, each ask with the key it bore.
func (f *arkNativeRouting) saw() []string {
	f.up.mu.Lock()
	defer f.up.mu.Unlock()
	return append([]string(nil), f.up.seen...)
}

// A primary key with no alias on it serves the native route: a plain
// generation, its stream, the task list, one task and a cancel, each
// signed with that key. This is the legal case the refusals below are
// measured against, on the stack they refuse on.
func TestRoutingAliasArkNativePlainKeyServes(t *testing.T) {
	f := standArkNative(t, "ark", "key", 0)
	if w := f.ask(t, "POST", NativePath+"ark/images/generations", `{"model":"doubao-seedream-4-5-251128","prompt":"四季"}`); w.Code != 200 || !strings.Contains(w.Body.String(), `"items"`) {
		t.Fatalf("generation: %d %s", w.Code, w.Body)
	}
	if w := f.ask(t, "POST", NativePath+"ark/images/generations", `{"model":"doubao-seedream-4-5-251128","prompt":"四季","stream":true}`); w.Code != 200 || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/event-stream") || !strings.Contains(w.Body.String(), "image_generation.completed") {
		t.Fatalf("stream: %d %q %s", w.Code, w.Header().Get("Content-Type"), w.Body)
	}
	if w := f.ask(t, "GET", NativePath+"ark/contents/generations/tasks?page_size=3", ""); w.Code != 200 {
		t.Fatalf("task list: %d %s", w.Code, w.Body)
	}
	if w := f.ask(t, "GET", NativePath+"ark/contents/generations/tasks/cgt-1", ""); w.Code != 200 {
		t.Fatalf("task get: %d %s", w.Code, w.Body)
	}
	if w := f.ask(t, "DELETE", NativePath+"ark/contents/generations/tasks/cgt-1", ""); w.Code != 200 {
		t.Fatalf("task delete: %d %s", w.Code, w.Body)
	}
	got := f.saw()
	if len(got) != 5 {
		t.Fatalf("Ark was asked %d times, want each of the five once: %q", len(got), got)
	}
	for _, ask := range got {
		if !strings.Contains(ask, "Bearer key") {
			t.Fatalf("an ask wasn't signed with the provider's plain primary key: %q", ask)
		}
	}
}

// An active alias on the primary key keeps that key from the ordinary
// native route: a generation, the task list, one task and a cancel are
// all answered locally, none of them sent. The key stays for the
// qualified reference that owns it; no fallback is asked for either.
func TestRoutingAliasArkNativeActiveAliasKeepsKeyFromOrdinaryRoute(t *testing.T) {
	f := standArkNative(t, "ark", "key", 0)
	if w := f.ask(t, "POST", NativePath+"ark/images/generations", `{"model":"doubao-seedream-4-5-251128","prompt":"四季"}`); w.Code != 200 {
		t.Fatalf("baseline generation before any alias: %d %s", w.Code, w.Body)
	}
	if w := f.ask(t, "GET", NativePath+"ark/contents/generations/tasks?page_size=3", ""); w.Code != 200 {
		t.Fatalf("baseline task list before any alias: %d %s", w.Code, w.Body)
	}
	if got := f.saw(); len(got) != 2 {
		t.Fatalf("baseline asks the plain key should have made: %q", got)
	}
	if err := provider.SetKeyRoutingAlias("ark", provider.KeyID("key"), "work"); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ name, method, path, body string }{
		{"generation", "POST", NativePath + "ark/images/generations", `{"model":"doubao-seedream-4-5-251128","prompt":"四季"}`},
		{"task list", "GET", NativePath + "ark/contents/generations/tasks?page_size=3", ""},
		{"task get", "GET", NativePath + "ark/contents/generations/tasks/cgt-1", ""},
		{"task delete", "DELETE", NativePath + "ark/contents/generations/tasks/cgt-1", ""},
	} {
		before := len(f.saw())
		w := f.ask(t, c.method, c.path, c.body)
		if got := f.saw(); len(got) != before {
			t.Errorf("%s consumed the primary key an active alias reserves: %q", c.name, got[before:])
		}
		if w.Code < 400 {
			t.Errorf("%s was served (%d) from a key an active alias reserves: %s", c.name, w.Code, w.Body)
		}
	}
}

// The native route takes its key only from a current providers file it
// can read. With that file unreadable and the hot routing-health cache
// still holding the last good state, an ordinary generation is refused
// rather than served from the backup, which no longer says what the user
// set — here an alias the backup predates.
func TestRoutingAliasArkNativeRejectsCurrentCorruptionWithoutInvalidation(t *testing.T) {
	f := standArkNative(t, "ark", "key", 0)
	const gen = `{"model":"doubao-seedream-4-5-251128","prompt":"四季"}`
	if w := f.ask(t, "POST", NativePath+"ark/images/generations", gen); w.Code != 200 {
		t.Fatalf("baseline generation before any alias: %d %s", w.Code, w.Body)
	}
	path := provider.Path()
	withoutAlias, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := provider.SetKeyRoutingAlias("ark", provider.KeyID("key"), "work"); err != nil {
		t.Fatal(err)
	}
	// The last good generation the unreadable file falls back to is from
	// before the alias was set.
	if err := os.WriteFile(path+".bak", withoutAlias, 0o600); err != nil {
		t.Fatal(err)
	}
	settled := time.Now().Add(-time.Hour)
	if err := os.Chtimes(path, settled, settled); err != nil {
		t.Fatal(err)
	}
	releaseHold := filememo.Hold()
	defer releaseHold()
	if err := provider.RoutingFileError(); err != nil {
		t.Fatalf("baseline routing health: %v", err)
	}
	current, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// Same size, same time: the health cache can't tell it changed. No
	// Forget, Touched or generation change — only the request and what it
	// consumed say what was served on.
	if err := os.WriteFile(path, bytes.Repeat([]byte("!"), len(current)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, settled, settled); err != nil {
		t.Fatal(err)
	}
	before := len(f.saw())
	w := f.ask(t, "POST", NativePath+"ark/images/generations", gen)
	if got := f.saw(); len(got) != before {
		t.Errorf("native route sent a key with the current providers file unreadable: %q", got[before:])
	}
	if w.Code < 400 {
		t.Errorf("native route served (%d) a generation whose routing state can't be known: %s", w.Code, w.Body)
	}
}

// A native generation waiting for its turn in the provider's minute is
// rechecked when the wait ends: the alias set on the primary key while
// the request waited keeps the key from it, with nothing sent.
func TestRoutingAliasArkNativeRPMWaitRechecksCurrentBinding(t *testing.T) {
	const id, key = "ark", "fixture-native-key"
	f := standArkNative(t, id, key, 1)
	clock := holdMinute(t)
	waiting := make(chan time.Duration, 8)
	previousTimer := rpmTimer
	rpmTimer = func(d time.Duration) (<-chan time.Time, func() bool) {
		c, stop := previousTimer(d)
		select {
		case waiting <- d:
		default:
		}
		return c, stop
	}
	t.Cleanup(func() { rpmTimer = previousTimer })
	const gen = `{"model":"doubao-seedream-4-5-251128","prompt":"四季"}`
	send := func(ctx context.Context) *httptest.ResponseRecorder {
		return f.ask(t, "POST", NativePath+id+"/images/generations", gen)
	}
	// A real successful send consumes the minute's only place.
	if w := send(t.Context()); w.Code != 200 {
		t.Fatalf("legal baseline generation: %d %s", w.Code, w.Body)
	}
	if got := f.saw(); len(got) != 1 || !strings.Contains(got[0], "Bearer "+key) {
		t.Fatalf("baseline send didn't use the plain primary key: %q", got)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	var wg sync.WaitGroup
	t.Cleanup(func() { cancel(); clock.release(); wg.Wait() })
	done := make(chan *httptest.ResponseRecorder, 1)
	wg.Add(1)
	go func() { defer wg.Done(); done <- send(ctx) }()
	select {
	case d := <-waiting:
		if d <= 0 {
			t.Fatalf("transport reported a nonpositive wait: %s", d)
		}
	case w := <-done:
		t.Fatalf("queued request ended before the transport RPM wait: %d %s", w.Code, w.Body)
	case <-ctx.Done():
		t.Fatal("queued request did not enter the transport RPM wait")
	}
	if got := f.saw(); len(got) != 1 {
		t.Fatalf("queued request was sent before RPM capacity was released: %q", got[1:])
	}
	t.Log("queued native generation entered a real transport RPM wait")

	if err := provider.SetKeyRoutingAlias(id, provider.KeyID(key), "work"); err != nil {
		t.Fatal(err)
	}
	clock.advance(rpmSpan()) // the minute's place comes back; the request stays live
	var w *httptest.ResponseRecorder
	select {
	case w = <-done:
		if ctx.Err() != nil {
			t.Fatalf("cancellation is not a routing result: %v", ctx.Err())
		}
	case <-ctx.Done():
		t.Fatal("queued request did not finish after RPM capacity was released")
	}
	if w.Code < 400 {
		t.Errorf("RPM waiter succeeded after its key became alias-reserved: %d %s", w.Code, w.Body)
	}
	if got := f.saw(); len(got) != 1 {
		t.Errorf("RPM waiter consumed the alias-reserved key after release: %q", got[1:])
	}
}
