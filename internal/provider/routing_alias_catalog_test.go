package provider

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

func TestRoutingAliasGroupPatternsStayOnBaseModels(t *testing.T) {
	routingAliasStoredFixture(t)
	for _, pattern := range []string{"alias-storage/*", "re:alias-storage/.*"} {
		t.Run(pattern, func(t *testing.T) {
			if err := SaveGroup(Group{ID: "alias-match", Name: "Alias match", Match: []string{pattern}}); err != nil {
				t.Fatal(err)
			}
			g, ms, ok := FindGroup("group/alias-match")
			if !ok || len(ms) == 0 {
				t.Fatalf("baseline pattern has no base members: ok=%v members=%v", ok, ms)
			}
			if !slices.Contains(g.Members, "alias-storage/m") {
				t.Errorf("pattern lost base member: %v", g.Members)
			}
			for _, member := range g.Members {
				if strings.Contains(member, "@") {
					t.Errorf("pattern implicitly included routing alias=%s", member)
				}
			}
			for _, member := range ms {
				if strings.Contains(member.Spelled(), "@") {
					t.Errorf("pattern route implicitly included alias=%s", member.Spelled())
				}
			}
		})
	}
}

func TestRoutingAliasGroupImportKeepsExplicitIdentity(t *testing.T) {
	routingAliasStoredFixture(t)
	share := func(name, member string) string {
		b, err := json.Marshal(SharedGroup{Magpie: ShareMark, Version: ShareVersion, Group: Group{Name: name, Members: []string{member}}})
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	if _, err := ImportGroup(share("Base import", "alias-storage/m")); err != nil {
		t.Fatalf("baseline base group import: %v", err)
	}
	got, err := ImportGroup(share("Qualified import", "alias-storage/m@work"))
	if err != nil {
		t.Errorf("known qualified member import failed: %v", err)
	} else {
		g, _, ok := FindGroup("group/" + got.ID)
		if !ok || !slices.Equal(g.Members, []string{"alias-storage/m@work"}) || len(got.Instead) != 0 {
			t.Errorf("qualified import changed identity: group=%+v result=%+v", g, got)
		}
	}
	missing, err := ImportGroup(share("Missing alias", "alias-storage/m@absent"))
	if err == nil {
		t.Errorf("unknown qualified-only import accepted: %+v", missing)
	}
	if !slices.Contains(missing.Missing, "alias-storage/m@absent") || len(missing.Instead) != 0 {
		t.Errorf("unknown alias must be missing, never remapped: %+v", missing)
	}
}

func TestRoutingAliasAgentSpellingsRoundTrip(t *testing.T) {
	routingAliasStoredFixture(t)
	p, err := Find("alias-storage")
	if err != nil {
		t.Fatal(err)
	}
	p.Models = []string{"claude-opus-4.6", "m"}
	if err = Save(*p); err != nil {
		t.Fatal(err)
	}
	const ref = "alias-storage/claude-opus-4.6@work"
	// The same adapter path must work for base entries before testing variants.
	if real, ok := ClaudeUndashed(ClaudeSpelled("alias-storage/claude-opus-4.6")); !ok || real != "alias-storage/claude-opus-4.6" {
		t.Fatalf("baseline Claude spelling=%q ok=%v", real, ok)
	}
	if real, ok := Unflat(FlatID("alias-storage/m")); !ok || real != "alias-storage/m" {
		t.Fatalf("baseline flat spelling=%q ok=%v", real, ok)
	}
	if real, ok := Unflat(FlatID(ref)); !ok || real != ref {
		t.Errorf("qualified flat spelling did not round-trip: got=%q ok=%v want=%q", real, ok, ref)
	}
	marked := ref + ":high[1m]"
	if real, ok := ClaudeUndashed(ClaudeSpelled(marked)); !ok || real != marked {
		t.Errorf("qualified dashed effort/window spelling=%q ok=%v; want %q", real, ok, marked)
	}

}

func TestRoutingAliasNativeLiteralAndConflict(t *testing.T) {
	routingAliasStoredFixture(t)
	p, err := Find("alias-storage")
	if err != nil {
		t.Fatal(err)
	}
	p.Models = []string{"m", "m@latest", "m@500k", "@cf/vendor/m"}
	if err = Save(*p); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ ref, model string }{
		{"alias-storage/m@latest", "m@latest"},
		{"alias-storage/m@500k@work", "m@500k"},
		{"alias-storage/@cf/vendor/m@work", "@cf/vendor/m"},
	} {
		_, model, ok := Resolve(tc.ref)
		if !ok || model != tc.model {
			t.Errorf("native/qualified %q resolved=%q ok=%v; want %q", tc.ref, model, ok, tc.model)
		}
	}
	p, err = Find("alias-storage")
	if err != nil {
		t.Fatal(err)
	}
	p.Models = append(p.Models, "m@work")
	if err = Save(*p); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := Resolve("alias-storage/m@work"); ok {
		t.Error("new native literal silently captured existing alias reference; expected ambiguity failure")
	}
}
