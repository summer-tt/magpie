package gui

import (
	"bytes"
	"os"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// Exercise the actual action mux: HTTP clients must receive the documented
// classification, independent of the internal error representation.
func TestRoutingAliasHTTPStatusClassifiesSetterFailures(t *testing.T) {
	for _, tc := range []struct {
		name, key, alias string
		want             int
	}{
		{"invalid-syntax", "key-one", "bad!", 400},
		{"missing-key", "not-a-saved-key", "fresh", 404},
		{"native-conflict", "key-one", "native", 409},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, post := aliasSetterKey(t)
			// This helper first proves the guarded gateway can use the configured key.
			// Then prove the new setter action itself is reachable and valid.
			if w := post("setter", "key-two", "work"); w.Code != 200 {
				t.Fatalf("baseline alias API: %d %s", w.Code, w.Body.String())
			}
			if tc.name == "native-conflict" {
				p, err := provider.Find("setter")
				if err != nil {
					t.Fatal(err)
				}
				p.Models = append(p.Models, "m@native")
				if err = provider.Save(*p); err != nil {
					t.Fatal(err)
				}
			}
			before, err := os.ReadFile(provider.Path())
			if err != nil {
				t.Fatal(err)
			}
			w := post("setter", tc.key, tc.alias)
			if w.Code != tc.want {
				t.Errorf("%s HTTP status=%d body=%s; want %d", tc.name, w.Code, w.Body.String(), tc.want)
			}
			after, err := os.ReadFile(provider.Path())
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, after) {
				t.Error("rejected alias setter changed providers.json")
			}
		})
	}
}
