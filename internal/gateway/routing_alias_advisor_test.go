package gateway

import (
	"strings"
	"testing"

	"github.com/tidwall/gjson"

	"github.com/yetone/magpie/internal/provider"
)

func TestRoutingAliasAdvisorUsesItsOwnModel(t *testing.T) {
	for _, tc := range []struct {
		name    string
		alias   bool
		allowed bool
	}{
		{"ordinary-advisor", false, true},
		{"qualified-advisor", true, true},
		{"denied-advisor", false, false},
		{"denied-qualified-advisor", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u := advisedSetup(t)
			if err := provider.SetKeyRoutingAlias("v", provider.KeyID("k"), "work"); err != nil {
				t.Fatal(err)
			}
			advisor := "adv/adv-pro"
			if tc.alias {
				if err := provider.SetKeyRoutingAlias("adv", provider.KeyID("k"), "review"); err != nil {
					t.Fatal(err)
				}
				advisor += "@review"
			}
			models := []string{"v/*"}
			if tc.allowed {
				models = append(models, advisor)
			}
			secret := advisorKey(t, models...)
			body := strings.Replace(advisorTurn("v/glm-5.1@work", advisor), `"stream":true`, `"stream":false`, 1)
			rec := sendAs(t, New(), secret, body)
			if rec.Code != 200 {
				t.Fatalf("%d %s", rec.Code, rec.Body)
			}
			u.mu.Lock()
			exec, adv := len(u.exec), len(u.advisor)
			u.mu.Unlock()
			wantAdv := 0
			if tc.allowed {
				wantAdv = 1
			}
			if exec != 2 || adv != wantAdv {
				t.Fatalf("executor requests %d, advisor requests %d; want 2, %d", exec, adv, wantAdv)
			}
			var result gjson.Result
			gjson.Get(rec.Body.String(), "content").ForEach(func(_, block gjson.Result) bool {
				if block.Get("type").String() == "advisor_tool_result" {
					result = block.Get("content")
				}
				return true
			})
			if tc.allowed {
				if result.Get("type").String() != "advisor_result" || result.Get("text").String() != "Check the tests first." {
					t.Fatalf("advisor result %s", result.Raw)
				}
			} else if result.Get("type").String() != "advisor_tool_result_error" {
				t.Fatalf("denied advisor result %s", result.Raw)
			}
		})
	}
}
