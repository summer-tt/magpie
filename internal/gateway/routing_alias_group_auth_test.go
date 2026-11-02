package gateway

import (
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/provider"
)

func TestRoutingAliasGroupOnlyGrantKeepsMembersAndAccountIntersection(t *testing.T) {
	for _, restrictToB := range []bool{false, true} {
		name := "group-only"
		if restrictToB {
			name = "group-and-account-b"
		}
		t.Run(name, func(t *testing.T) {
			v, h, update := routingAliasFixture(t)
			update(map[string]any{"a": routingAliasBinding("k-work", true), "b": routingAliasBinding("k-ordinary", true)})
			if err := provider.SaveGroup(provider.Group{ID: "alias-granted", Name: "Alias granted", Members: []string{"alias-relay/m@a", "alias-relay/m@b"}, Routing: provider.Ordered}); err != nil {
				t.Fatal(err)
			}
			keys, secrets := newCaller(t, "Fixture group caller")
			send := func(model string) *httptest.ResponseRecorder {
				r := httptest.NewRequest("POST", "http://localhost/v1/chat/completions", strings.NewReader(`{"model":"`+model+`","messages":[{"role":"user","content":"hello"}]}`))
				r.RemoteAddr = "127.0.0.1:1234"
				r.Header.Set("Authorization", "Bearer "+secrets[0])
				w := httptest.NewRecorder()
				h.ServeHTTP(w, r)
				return w
			}
			if w := send("group/alias-granted"); w.Code != 200 {
				t.Fatalf("baseline unrestricted caller group: %d %s", w.Code, w.Body.String())
			}
			if got := v.take(); !slices.Equal(got, []routingAliasCall{{"k-work", "m"}}) {
				t.Fatalf("baseline group owner/plain model=%v", got)
			}
			if _, err := access.Update("models-key", access.Change{Key: keys[0].ID, Models: []string{"group/alias-granted"}}); err != nil {
				t.Fatal(err)
			}
			wantKey := "k-work"
			if restrictToB {
				wantKey = "k-ordinary"
				if _, err := access.Update("accounts-key", access.Change{Key: keys[0].ID, Accounts: []string{"alias-relay/" + provider.KeyID(wantKey)}}); err != nil {
					t.Fatal(err)
				}
			}
			w := send("group/alias-granted")
			got := v.take()
			if w.Code != 200 {
				t.Errorf("group-only model grant should authorize its qualified members: status=%d body=%s", w.Code, w.Body.String())
			}
			if !slices.Equal(got, []routingAliasCall{{wantKey, "m"}}) {
				t.Errorf("group grant/account intersection calls=%v want only owner=%s plain=m", got, wantKey)
			}
			w = send("alias-relay/m")
			got = v.take()
			if w.Code != 403 || len(got) != 0 {
				t.Errorf("group-only grant expanded to ordinary provider model: status=%d calls=%v", w.Code, got)
			}
		})
	}
}
