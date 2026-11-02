package gateway

import (
	"encoding/json"
	"os"
	"slices"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// A provider kept for its usage only (Select none: NoModels with no
// model picked) serves none of its models: the ordinary
// "provider/model" reference is refused by resolveIn's ServesNone
// condition. A qualified reference must meet the same refusal — the
// alias resolver accepting what the provider itself does not serve
// would let an old "p/m@work" reference keep generating after the user
// put the provider on usage only. The binding registry and its owner
// history survive the edit untouched.
func TestRoutingAliasUsageOnlyStopsQualifiedAndOrdinary(t *testing.T) {
	v, h, _ := routingAliasFixture(t)
	// positive baselines: with the binding active the qualified reference
	// reaches its bound key, and the ordinary one the ordinary pool
	if w := routingAliasSend(h, "alias-relay/m@work", ""); w.Code != 200 {
		t.Fatalf("baseline qualified: %d %s", w.Code, w.Body.String())
	}
	if got := v.take(); !slices.Equal(got, []routingAliasCall{{"k-work", "m"}}) {
		t.Fatalf("baseline qualified upstream = %v", got)
	}
	if w := routingAliasSend(h, "alias-relay/m", ""); w.Code != 200 {
		t.Fatalf("baseline ordinary: %d %s", w.Code, w.Body.String())
	}
	if got := v.take(); !slices.Equal(got, []routingAliasCall{{"k-ordinary", "m"}}) {
		t.Fatalf("baseline ordinary upstream = %v", got)
	}

	// Select none: usage only, nothing picked
	p, err := provider.Find("alias-relay")
	if err != nil {
		t.Fatal(err)
	}
	p.NoModels = true
	p.Models = nil
	if err := provider.Save(*p); err != nil {
		t.Fatal(err)
	}

	// control: the ordinary reference is already refused by the existing
	// ServesNone rule, with nothing consumed
	if w := routingAliasSend(h, "alias-relay/m", ""); w.Code < 400 {
		t.Errorf("ordinary reference on a usage-only provider generated: %d", w.Code)
	}
	if got := v.take(); len(got) != 0 {
		t.Errorf("ordinary reference consumed upstream: %v", got)
	}

	// the qualified reference must be refused the same way: the alias
	// binding selects an account, never a model the provider doesn't serve
	if w := routingAliasSend(h, "alias-relay/m@work", ""); w.Code < 400 {
		t.Errorf("qualified reference on a usage-only provider generated: %d %s", w.Code, w.Body.String())
	}
	if got := v.take(); len(got) != 0 {
		t.Errorf("qualified reference consumed upstream: %v; the binding must not widen ServesNone", got)
	}

	// the registry, active binding and owner history survive the edit
	b, err := os.ReadFile(provider.Path())
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Providers []struct {
			ID         string `json:"id"`
			KeyAliases map[string]struct {
				OwnerID string `json:"ownerId"`
				KeyID   string `json:"keyId"`
				Active  bool   `json:"active"`
			} `json:"keyAliases"`
		} `json:"providers"`
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		t.Fatal(err)
	}
	for _, row := range cfg.Providers {
		if row.ID != "alias-relay" {
			continue
		}
		binding, ok := row.KeyAliases["work"]
		if !ok || !binding.Active || binding.KeyID != provider.KeyID("k-work") || binding.OwnerID != provider.KeyID("k-work") {
			t.Errorf("usage-only edit changed the registry: work = %+v", binding)
		}
		return
	}
	t.Error("usage-only edit lost the alias-relay row")
}
