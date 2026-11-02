package provider

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/testenv"
)

// A reading begun before a plan's settings changed publishes nothing — not
// to the history file, and not into the history this process wrote last,
// which the next valid reading of another account writes back with its
// own. The first write makes the file this process's own, so heldQuotaHist
// answers with that map instead of reading the file again (the state the
// older clear test never reached); the generation refusal at publishReading
// then comes after the map has already been changed.
func TestQuotaHistoryStaleReadingDoesNotReachTheSharedCache(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t0 := time.Date(2026, 11, 2, 8, 0, 0, 0, time.UTC)
	r5, rw := t0.Add(3*time.Hour), t0.Add(4*24*time.Hour)
	at := func(d time.Duration) time.Time { return t0.Add(d) }
	reads := 0
	oldRead := readQuotaHist
	readQuotaHist = func() quotaHist {
		reads++
		return oldRead()
	}
	t.Cleanup(func() { readQuotaHist = oldRead })
	onDisk := func() quotaHist {
		t.Helper()
		b, err := os.ReadFile(quotaHistPath())
		if err != nil {
			t.Fatalf("reading the history file: %v", err)
		}
		h, _ := parseQuotaHist(b)
		return h
	}

	// a legal reading of one account: the file is written, and from here
	// on the history this process wrote is the cache heldQuotaHist answers
	// with, not a fresh file read
	noteQuotaHistory([]SubscriptionQuota{histCard("A@x.com", 10, r5, 40, rw)}, at(0))
	if reads != 1 {
		t.Fatalf("the first reading read the history file %d times, want once", reads)
	}
	if h := onDisk(); len(h["codex|a@x.com"]["5 hours"]) != 1 || h["codex|a@x.com"]["5 hours"][0].Left != 90 {
		t.Fatalf("the legal reading was not written: %+v", h)
	}
	quotaHistMu.Lock()
	wrote, _ := quotaHistWrote(quotaHistPath())
	quotaHistMu.Unlock()
	if !wrote {
		t.Fatal("the file is not the one this process wrote: heldQuotaHist would read it instead of answering with the shared map")
	}
	reads = 0

	// a reading begun before the clear: its context carries the generation
	// the settings had, and ForgetPlanQuotas moves the generation on before
	// the reading reaches the history
	stale, _ := quotaReading(context.Background())
	ForgetPlanQuotas()

	// the stale reading runs after the clear. It replaces A's last point of
	// each window in place (within quotaHistSame, same cycle), adds a window
	// to A's account and adds a new account B; publishReading then refuses
	// the write.
	staleA := histCard("A@x.com", 20, r5, 40, rw)
	staleA.Windows = append(staleA.Windows, QuotaWindow{Name: "Monthly", Used: 5, ResetsAt: &rw})
	noteQuotaHistoryRead(stale, []SubscriptionQuota{staleA, histCard("B@x.com", 30, r5, 40, rw)}, at(10*time.Second))
	if reads != 0 {
		t.Errorf("the stale reading read the history file %d times instead of the history this process wrote", reads)
	}
	// the refusal itself held: nothing of the reading is in the file yet
	if h := onDisk(); len(h["codex|a@x.com"]["5 hours"]) != 1 || h["codex|a@x.com"]["5 hours"][0].Left != 90 {
		t.Fatalf("the refused reading changed the file: %+v", h)
	} else if _, ok := h["codex|b@x.com"]; ok {
		t.Fatalf("the refused reading's account reached the file: %+v", h)
	}
	// nor in the shared history the next valid reading writes back
	quotaHistMu.Lock()
	held := heldQuotaHist()
	quotaHistMu.Unlock()
	if pts, ok := held["codex|a@x.com"]["5 hours"]; !ok || len(pts) != 1 || pts[0].Left != 90 || !pts[0].At.Equal(t0) {
		t.Errorf("the refused reading changed the shared history: A's 5 hours = %+v; want one point left 90 at %v", pts, t0)
	}
	if _, ok := held["codex|a@x.com"]["Monthly"]; ok {
		t.Errorf("the refused reading added a window to the shared history: %+v", held["codex|a@x.com"]["Monthly"])
	}
	if pts, ok := held["codex|b@x.com"]; ok {
		t.Errorf("the refused reading added its account to the shared history: %+v", pts)
	}

	// another account reads validly after the clear: its own point is
	// written, and with it whatever the refused reading left in the shared
	// history
	noteQuotaHistory([]SubscriptionQuota{histCard("D@x.com", 50, r5, 40, rw)}, at(time.Minute))
	if reads != 0 {
		t.Errorf("the valid reading read the history file %d times instead of the history this process wrote", reads)
	}
	h := onDisk()
	if pts := h["codex|a@x.com"]["5 hours"]; len(pts) != 1 || pts[0].Left != 90 || !pts[0].At.Equal(t0) {
		t.Errorf("a valid reading's write carried the refused reading's point of A: 5 hours = %+v; want one point left 90 at %v", pts, t0)
	}
	if pts := h["codex|a@x.com"]["Weekly"]; len(pts) != 1 || pts[0].Left != 60 || !pts[0].At.Equal(t0) {
		t.Errorf("a valid reading's write moved A's Weekly point: %+v; want one point left 60 at %v", pts, t0)
	}
	if pts, ok := h["codex|a@x.com"]["Monthly"]; ok {
		t.Errorf("a valid reading's write carried the refused reading's window: %+v", pts)
	}
	if pts, ok := h["codex|b@x.com"]; ok {
		t.Errorf("a valid reading's write carried the refused reading's account: %+v", pts)
	}
	if pts := h["codex|d@x.com"]["5 hours"]; len(pts) != 1 || pts[0].Left != 50 {
		t.Errorf("the valid reading after the clear was not written: 5 hours = %+v", pts)
	}
}
