package gateway

import (
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

func TestRoutingAliasSpellingsReachTheGateway(t *testing.T) {
	v, h, update := routingAliasFixture(t)
	p, err := provider.Find("alias-relay")
	if err != nil {
		t.Fatal(err)
	}
	p.Models = []string{"m", "claude-opus-4.6"}
	if err = provider.Save(*p); err != nil {
		t.Fatal(err)
	}
	const base = "alias-relay/claude-opus-4.6"
	if w := routingAliasSend(h, base, ""); w.Code != 200 {
		t.Fatalf("baseline Claude gateway: %d %s", w.Code, w.Body.String())
	}
	if got := v.take(); !slices.Equal(got, []routingAliasCall{{"k-ordinary", "claude-opus-4.6"}}) {
		t.Fatalf("baseline Claude upstream=%v", got)
	}
	ref := base + "@work"
	spellings := []string{provider.FlatID(ref), provider.ClaudeSpelled(ref), ref + ":high[1m]"}
	for _, ref := range spellings {
		w := routingAliasSend(h, ref, "")
		if w.Code != 200 {
			t.Errorf("qualified spelling %q gateway status=%d", ref, w.Code)
		}
		if got := v.take(); !slices.Equal(got, []routingAliasCall{{"k-work", "claude-opus-4.6"}}) {
			t.Errorf("qualified spelling %q upstream=%v; want original target and plain model", ref, got)
		}
	}
	update(map[string]any{"work": routingAliasBinding("k-work", false)})
	for _, ref := range spellings {
		w := routingAliasSend(h, ref, "")
		if w.Code != 404 || !strings.Contains(strings.ToLower(w.Body.String()), "alias") {
			t.Errorf("retired spelling %q status=%d; want specific alias404", ref, w.Code)
		}
		if got := v.take(); len(got) != 0 {
			t.Errorf("retired spelling %q consumed upstream=%v", ref, got)
		}
	}
}
