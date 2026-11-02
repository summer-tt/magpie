package provider

import (
	"context"
	"testing"
	"time"
)

func TestKeepLast(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	restart := func() {
		lastQuotas.Lock()
		lastQuotas.m, lastQuotas.loaded = nil, false
		lastQuotas.Unlock()
	}
	restart()
	defer restart()
	later := time.Now().Add(time.Hour)
	good := SubscriptionQuota{Provider: "claude", Name: "Claude Code", User: "A@x.com", Windows: []QuotaWindow{
		{Name: "5 hours", Used: 40, ResetsAt: &later, Span: 5 * time.Hour},
		{Name: "7 days · Opus", Used: 10, Model: "opus", Span: 7 * 24 * time.Hour},
	}}
	if q := keepLast(context.Background(), good, ""); q.AsOf != nil || q.Windows[0].Used != 40 {
		t.Fatalf("%+v", q)
	}
	restart() // what was read is on disk

	limited := SubscriptionQuota{Provider: "claude", Name: "Claude Code", User: "a@x.com", Windows: []QuotaWindow{},
		Error: errClaudeNotAsked.Error()}
	q := keepLast(context.Background(), limited, "")
	if q.Error != "" || q.AsOf == nil || len(q.Windows) != 2 || q.Windows[0].Used != 40 || q.Windows[1].Model != "opus" || q.Windows[0].Span != 5*time.Hour {
		t.Fatalf("not asked: %+v", q)
	}

	// the same through LoginUsage, which knows the user but not the name
	q = keepLast(context.Background(), SubscriptionQuota{Provider: "claude", Windows: []QuotaWindow{}, Error: "Too Many Requests"}, "a@x.com")
	if q.Error != "" || q.Name != "Claude Code" || q.Windows[0].Used != 40 {
		t.Fatalf("login: %+v", q)
	}

	// a sign-in gone bad is said, not covered up
	if q := keepLast(context.Background(), SubscriptionQuota{Provider: "claude", User: "a@x.com", Error: "Claude Code's sign-in has expired"}, ""); q.Error == "" {
		t.Fatalf("expired: %+v", q)
	}

	// a new reading is used at once
	good.Windows[0].Used = 70
	keepLast(context.Background(), good, "")
	if q := keepLast(context.Background(), limited, ""); q.Windows[0].Used != 70 {
		t.Fatalf("newer: %+v", q)
	}

	// a window that has reset since starts again from nothing
	past := time.Now().Add(-time.Minute)
	good.Windows[0].ResetsAt = &past
	keepLast(context.Background(), good, "")
	if q := keepLast(context.Background(), limited, ""); q.Windows[0].Used != 0 {
		t.Fatalf("reset: %+v", q)
	}
}

// A plan that has ended and will not renew has no window starting again:
// a reading kept from before stands as it was read, not at nothing, while
// one that renews is read as a new window.
func TestKeepLastPlanOverStandsAsRead(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	restart := func() {
		lastQuotas.Lock()
		lastQuotas.m, lastQuotas.loaded = nil, false
		lastQuotas.Unlock()
	}
	restart()
	defer restart()
	ended := time.Now().Add(-time.Hour)
	reset := ended
	lapsed := SubscriptionQuota{Provider: "bailian-token-plan", Name: "Bailian Token Plan", Until: &ended, Renew: "off",
		Windows: []QuotaWindow{{Name: "Month", Used: 56, Limit: 180000, Amount: 100800, Unit: "credits", ResetsAt: &reset}}}
	const tag = "bailian personal the-key"
	if q := keepLast(context.Background(), lapsed, tag); q.AsOf != nil {
		t.Fatalf("a reading just made came back dated: %+v", q)
	}
	failed := SubscriptionQuota{Provider: "bailian-token-plan", Name: "Bailian Token Plan", Windows: []QuotaWindow{},
		Error: "Bailian: the console gateway answered 503 Service Unavailable"}
	q := keepLast(context.Background(), failed, tag)
	if q.AsOf == nil || len(q.Windows) != 1 || q.Windows[0].Used != 56 || q.Windows[0].ResetsAt == nil ||
		q.Windows[0].Amount != 100800 || q.Windows[0].Limit != 180000 {
		t.Fatalf("an ended, non-renewing plan was read as started again: %+v", q)
	}

	// one that renews is a window that started again
	renewing := lapsed
	renewing.Renew = "auto"
	keepLast(context.Background(), renewing, tag)
	if q := keepLast(context.Background(), failed, tag); len(q.Windows) != 1 || q.Windows[0].Used != 0 || q.Windows[0].ResetsAt != nil {
		t.Fatalf("a renewing plan did not start again: %+v", q)
	}
}

// A read begun before the plans were forgotten is not kept: its answer is
// of an account or a key no longer in use.
func TestKeepReadingAfterForgetKeepsNothing(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	lastQuotas.Lock()
	lastQuotas.m, lastQuotas.loaded = nil, false
	lastQuotas.Unlock()
	defer func() {
		lastQuotas.Lock()
		lastQuotas.m, lastQuotas.loaded = nil, false
		lastQuotas.Unlock()
	}()
	ctx, _ := quotaReading(context.Background())
	forgetPlanQuotas()
	before := time.Now().Add(time.Hour)
	read := SubscriptionQuota{Provider: "bailian-token-plan", Name: "Bailian Token Plan",
		Windows: []QuotaWindow{{Name: "Month", Used: 56, ResetsAt: &before}}}
	if q := keepReading(ctx, read, "bailian personal the-key"); q.AsOf != nil || !q.Windows[0].ResetsAt.Equal(before) {
		t.Fatalf("a read begun before the forget was changed: %+v", q)
	}
	if q := keepReading(context.Background(), SubscriptionQuota{Provider: "bailian-token-plan",
		Windows: []QuotaWindow{}, Error: "503 Service Unavailable"}, "bailian personal the-key"); q.AsOf != nil || len(q.Windows) != 0 {
		t.Fatalf("a read begun before the forget was kept: %+v", q)
	}
	// a read begun after it is kept as before
	later, _ := quotaReading(context.Background())
	keepReading(later, read, "bailian personal the-key")
	if q := keepReading(context.Background(), SubscriptionQuota{Provider: "bailian-token-plan",
		Windows: []QuotaWindow{}, Error: "503 Service Unavailable"}, "bailian personal the-key"); q.AsOf == nil || len(q.Windows) != 1 {
		t.Fatalf("a read begun after the forget was not kept: %+v", q)
	}
}
