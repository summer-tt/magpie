package gateway

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// nativeCompactIdentityAsk is what a fake ChatGPT backend was asked: the
// account id and bearer that actually signed the request.
type nativeCompactIdentityAsk struct {
	account, bearer, path string
}

// nativeCompactIdentityFixture stands in for the ChatGPT backend and sends
// a legal native compaction reply, recording each request's real signing
// headers.
func nativeCompactIdentityFixture(t *testing.T) func(bearer, account, pin string) (int, string, []nativeCompactIdentityAsk) {
	t.Helper()
	codexSignedIn(t, "spare@example.com")
	var mu sync.Mutex
	var asks []nativeCompactIdentityAsk
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		asks = append(asks, nativeCompactIdentityAsk{
			account: r.Header.Get("chatgpt-account-id"),
			bearer:  r.Header.Get("Authorization"),
			path:    r.URL.Path,
		})
		mu.Unlock()
		if r.URL.Path != "/backend-api/codex/responses/compact" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"output":[{"type":"compaction","encrypted_content":"gAAAAAx"}]}`)
	}))
	t.Cleanup(up.Close)
	was := provider.CodexBase
	provider.CodexBase = up.URL + "/backend-api/codex"
	t.Cleanup(func() { provider.CodexBase = was })

	h := New().Front()
	take := func() []nativeCompactIdentityAsk {
		mu.Lock()
		defer mu.Unlock()
		out := slices.Clone(asks)
		asks = nil
		return out
	}
	return func(bearer, account, pin string) (int, string, []nativeCompactIdentityAsk) {
		t.Helper()
		take()
		r := httptest.NewRequest(http.MethodPost, "http://localhost"+CodexPath+"/responses/compact", strings.NewReader(`{"model":"gpt-5.5","input":[{"type":"message","role":"user","content":"ping"}]}`))
		r.RemoteAddr = "127.0.0.1:12345"
		r.Header.Set("Authorization", "Bearer "+bearer)
		r.Header.Set("chatgpt-account-id", account)
		if pin != "" {
			r.Header.Set(AccountHeader, pin)
		}
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code, w.Body.String(), take()
	}
}

// nativeCompactSavedBearer is the fake access token saved for one of
// Codex's other accounts, read in memory and never printed.
func nativeCompactSavedBearer(t *testing.T, user string) string {
	t.Helper()
	p, err := provider.Find("codex")
	if err != nil {
		t.Fatal(err)
	}
	for _, on := range p.AlsoOn() {
		if !strings.EqualFold(on.Account.User, user) {
			continue
		}
		tok, ok, err := on.Account.Token(context.Background())
		if err != nil || !ok || tok == "" {
			t.Fatalf("saved Codex account %s has no token: %v", user, err)
		}
		return tok
	}
	t.Fatalf("no saved Codex account %s beside the sign-in", user)
	return ""
}

// A native compaction's caller identity is the bearer that made it. The
// default sign-in's eligibility cannot authorize another managed account's
// bearer, an unmanaged bearer keeps its own relay, and X-Magpie-Account
// keeps a paused sign-in from being answered by another account.
func TestRoutingAliasCodexNativeCompactKeepsCallerIdentity(t *testing.T) {
	t.Run("managed spare bearer is not spent through the ordinary sign-in", func(t *testing.T) {
		compact := nativeCompactIdentityFixture(t)
		bearer := nativeCompactSavedBearer(t, "spare@example.com")

		code, body, asks := compact(bearer, "acct-2", "")
		if code != http.StatusOK || !strings.Contains(body, `"type":"compaction"`) ||
			len(asks) != 1 || asks[0].account != "acct-2" || asks[0].bearer != "Bearer "+bearer || asks[0].path != "/backend-api/codex/responses/compact" {
			t.Fatalf("baseline spare compaction: status=%d body=%s asks=%+v; want the spare's own bearer to answer", code, body, asks)
		}
		if err := provider.SetAccountRoutingAlias("codex", provider.LoginID("codex", "spare@example.com"), "work"); err != nil {
			t.Fatal(err)
		}
		code, body, asks = compact(bearer, "acct-2", "")
		if code < http.StatusBadRequest || len(asks) != 0 {
			t.Errorf("ordinary compaction on the aliased spare's bearer: status=%d body=%s asks=%+v; want a local refusal and no upstream request", code, body, asks)
		}
	})

	t.Run("unmanaged bearer keeps its own relay", func(t *testing.T) {
		compact := nativeCompactIdentityFixture(t)
		bearer := "unmanaged-chatgpt-token"
		code, body, asks := compact(bearer, "acct-C", "")
		if code != http.StatusOK || !strings.Contains(body, `"type":"compaction"`) ||
			len(asks) != 1 || asks[0].account != "acct-C" || asks[0].bearer != "Bearer "+bearer {
			t.Fatalf("baseline unmanaged compaction: status=%d body=%s asks=%+v; want the unmanaged bearer and account kept", code, body, asks)
		}
		if err := provider.SetAccountRoutingAlias("codex", provider.LoginID("codex", "me@example.com"), "work"); err != nil {
			t.Fatal(err)
		}

		code, body, asks = compact(bearer, "acct-C", "")
		if code != http.StatusOK || !strings.Contains(body, `"type":"compaction"`) ||
			len(asks) != 1 || asks[0].account != "acct-C" || asks[0].bearer != "Bearer "+bearer {
			t.Errorf("unmanaged compaction beside an aliased sign-in: status=%d body=%s asks=%+v; want the unmanaged bearer and account kept", code, body, asks)
		}
	})

	t.Run("pinned paused sign-in is not answered by the spare", func(t *testing.T) {
		compact := nativeCompactIdentityFixture(t)
		bearer := codexAccessToken(t)
		code, body, asks := compact(bearer, "acct-1", "me@example.com")
		if code != http.StatusOK || !strings.Contains(body, `"type":"compaction"`) ||
			len(asks) != 1 || asks[0].account != "acct-1" || asks[0].bearer != "Bearer "+bearer {
			t.Fatalf("baseline pinned compaction: status=%d body=%s asks=%+v; want the pinned sign-in's own bearer to answer", code, body, asks)
		}
		if err := provider.SetLoginOn("codex", "me@example.com", false); err != nil {
			t.Fatal(err)
		}

		code, body, asks = compact(bearer, "acct-1", "me@example.com")
		if code < http.StatusBadRequest || len(asks) != 0 {
			t.Errorf("pinned paused compaction: status=%d body=%s asks=%+v; want a local error and no account replaced by the spare", code, body, asks)
		}
	})
}
