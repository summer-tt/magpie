package provider

import (
	"errors"
	"os"
	"slices"
	"testing"
)

// The setter's native-conflict check compares the alias against native
// model IDs case-sensitively while the resolver normalizes the request
// suffix: an alias "work" may be saved beside a native "m@WORK", and the
// previously working native reference then answers a conflict error.
// Saving must refuse an alias whose normalized form collides with a
// known native ID's trailing suffix, and a refusal must leave the file
// and the native reference as they were.
func TestRoutingAliasSetterRejectsCaseVariantNativeSuffix(t *testing.T) {
	azureHome(t)
	if err := Save(Provider{ID: "cased", Name: "Cased", Key: "k1", Chat: "http://127.0.0.1:1/v1", Models: []string{"m@WORK"}}); err != nil {
		t.Fatal(err)
	}
	// positive baseline: the native model with its case is resolvable and
	// kept whole
	p, m, err := ResolveModel("cased/m@WORK")
	if err != nil || p.ID != "cased" || m != "m@WORK" {
		t.Fatalf("native baseline: %v %q %q", err, p.ID, m)
	}
	// positive baseline: an alias that clashes with no native id saves
	if err := SetKeyRoutingAlias("cased", KeyID("k1"), "team"); err != nil {
		t.Fatalf("a non-conflicting alias must save: %v", err)
	}
	if _, m, err := ResolveModel("cased/m@WORK"); err != nil || m != "m@WORK" {
		t.Fatalf("native after an unrelated alias: %v %q", err, m)
	}

	// the case-variant alias must be refused at save time
	before, err := os.ReadFile(Path())
	if err != nil {
		t.Fatal(err)
	}
	err = SetKeyRoutingAlias("cased", KeyID("k1"), "work")
	var re *RoutingError
	if !errors.As(err, &re) || re.Status != 409 {
		t.Errorf("alias %q against native %q: err = %v; want a 409 conflict at save time (the resolver normalizes the suffix)", "work", "m@WORK", err)
	}
	after, err := os.ReadFile(Path())
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Error("a refused alias changed providers.json; the file must keep its bytes")
	}
	if _, m, err := ResolveModel("cased/m@WORK"); err != nil || m != "m@WORK" {
		t.Errorf("native m@WORK after the refusal: %v %q; it must stay usable as a native model", err, m)
	}
}

// The same-case collision the setter already refuses must stay refused:
// a native "m@team" and an alias "team" cannot coexist either.
func TestRoutingAliasSetterStillRejectsSameCaseNativeSuffix(t *testing.T) {
	azureHome(t)
	if err := Save(Provider{ID: "cased", Name: "Cased", Key: "k1", Chat: "http://127.0.0.1:1/v1", Models: []string{"m@team"}}); err != nil {
		t.Fatal(err)
	}
	err := SetKeyRoutingAlias("cased", KeyID("k1"), "team")
	var re *RoutingError
	if !errors.As(err, &re) || re.Status != 409 {
		t.Errorf("alias %q against native %q: err = %v; want a 409 conflict", "team", "m@team", err)
	}
}

// A direct qualified request never expands its provider's fallback
// (planPaused), so the prechecks that read FallbackModels — redaction,
// native compaction, cap careful — must see none for a provider carrying
// a routing selection. An ordinary provider keeps what it configured:
// the model fallback and its explicit group's members, a qualified
// member among them with its full identity.
func TestRoutingAliasQualifiedFallbackModelsStayUnexpanded(t *testing.T) {
	azureHome(t)
	for _, p := range []Provider{
		{ID: "fb", Name: "Fb", Key: "k-fb", Keys: []KeyAccount{{Key: "k-fb-work"}}, Chat: "http://127.0.0.1:1/v1", Models: []string{"m"}, Fallback: []string{"spare/m", "group/explicit"}},
		{ID: "spare", Name: "Spare", Key: "k-spare", Chat: "http://127.0.0.1:2/v1", Models: []string{"m"}},
	} {
		if err := Save(p); err != nil {
			t.Fatal(err)
		}
	}
	if err := SetKeyRoutingAlias("fb", KeyID("k-fb-work"), "work"); err != nil {
		t.Fatal(err)
	}
	if err := SetKeyRoutingAlias("spare", KeyID("k-spare"), "later"); err != nil {
		t.Fatal(err)
	}
	if err := SaveGroup(Group{ID: "explicit", Name: "Explicit", Members: []string{"spare/m@later"}, Routing: Ordered}); err != nil {
		t.Fatal(err)
	}

	// the ordinary provider's fallback precheck keeps both members
	normal, err := Find("fb")
	if err != nil {
		t.Fatal(err)
	}
	var refs []string
	for _, m := range FallbackModels(*normal) {
		refs = append(refs, m.Provider.ModelRef(m.Model))
	}
	if !slices.Contains(refs, "spare/m") || !slices.Contains(refs, "spare/m@later") {
		t.Fatalf("ordinary fallback members = %v; want the named model and the explicit group's qualified member", refs)
	}

	// the qualified copy a direct request resolves to expands nothing
	q, m, err := ResolveModel("fb/m@work")
	if err != nil || m != "m" {
		t.Fatalf("qualified resolve: %v %q", err, m)
	}
	if q.RoutingAlias() != "work" {
		t.Fatalf("resolved provider carries no selection: %q", q.RoutingAlias())
	}
	if got := FallbackModels(q); len(got) != 0 {
		var gotRefs []string
		for _, x := range got {
			gotRefs = append(gotRefs, x.Provider.ModelRef(x.Model))
		}
		t.Errorf("qualified fallback precheck read %v; a request that never expands its provider's fallback must see none", gotRefs)
	}
}
