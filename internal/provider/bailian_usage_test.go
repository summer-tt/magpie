package provider

// Bailian's Token Plan 个人版 tells its month only through its console
// gateway, traded for the account's AccessKey ID and Secret
// (bailian_usage.go). The fixtures preserve the response shapes observed
// from the service, but all credentials, usage shares and subscription
// timestamps are synthetic. No test reaches the network: the fake
// vendor is a httptest server reached through rewrite.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/agentenv"
)

// The signature the official CLI's own signer makes (modelstudioai/cli
// packages/core/src/client/acs.ts signAcsRequest), and what an
// implementation of Alibaba Cloud's documented ACS3 algorithm makes of
// the same request, byte for byte: a runtime with a UUID nonce and a
// clock of its own was given these two instead.
func TestACS3SignMatchesCLI(t *testing.T) {
	date := time.Date(2026, 10, 27, 8, 9, 10, 0, time.UTC)
	got := acs3Sign("LTAI5tExampleAKID", "SKexample00000000000000000000000000",
		"modelstudio.cn-beijing.aliyuncs.com", "/modelstudio/cli/generateAccessToken",
		"GenerateCLIAccessToken", "2026-02-10", nil, date, "b7f6a2c3d4e5f60718293a4b5c6d7e8f")
	want := "ACS3-HMAC-SHA256 Credential=LTAI5tExampleAKID,SignedHeaders=content-type;host;x-acs-action;x-acs-content-sha256;x-acs-date;x-acs-signature-nonce;x-acs-version,Signature=1b5e35aefc1c531c02bc434ffd84ec1d7464ae58194a385dc3698a15aa59c2d9"
	if got != want {
		t.Fatalf("\n got %s\nwant %s", got, want)
	}
	// the same moment in another zone signs with the UTC date
	cst := date.In(time.FixedZone("CST", 8*3600))
	if acs3Sign("LTAI5tExampleAKID", "SKexample00000000000000000000000000",
		"modelstudio.cn-beijing.aliyuncs.com", "/modelstudio/cli/generateAccessToken",
		"GenerateCLIAccessToken", "2026-02-10", nil, cst, "b7f6a2c3d4e5f60718293a4b5c6d7e8f") != want {
		t.Error("a time in another zone signed differently")
	}
}

// Only Bailian's Token Plan endpoints take an access key: the personal
// plan at token-plan.cn-beijing.maas.aliyuncs.com. The Qwen AI platform's
// token plan is another vendor's, and neither pay as you go nor another
// host takes one; Volcengine's and Tencent Cloud's stay theirs.
func TestBailianAccessKeyVendor(t *testing.T) {
	const root = "https://token-plan.cn-beijing.maas.aliyuncs.com"
	for _, c := range []struct {
		p      Provider
		vendor string
	}{
		{Provider{Chat: root + "/compatible-mode/v1"}, "bailian"},
		{Provider{Anthropic: root + "/apps/anthropic"}, "bailian"},
		{Provider{Responses: root + "/v1"}, "bailian"},
		{Provider{Chat: "https://Token-Plan.CN-Beijing.Maas.Aliyuncs.com/compatible-mode/v1"}, "bailian"},
		{Provider{Chat: "https://token-plan.maas.qianwenaiapi.com/compatible-mode/v1"}, ""},
		{Provider{Chat: "https://dashscope.aliyuncs.com/compatible-mode/v1"}, ""},
		{Provider{Chat: "https://token-plan.cn-beijing.maas.aliyuncs.com.evil.test/v1"}, ""},
		{Provider{Chat: root + "/compatible-mode/v1", Account: &Account{}}, ""},
		{Provider{Chat: "https://ark.cn-beijing.volces.com/api/coding/v3"}, "volcengine"},
		{Provider{Chat: "https://tokenhub.tencentmaas.com/plan/v3"}, "tencent"},
	} {
		if got := AccessKeyVendor(c.p); got != c.vendor {
			t.Errorf("%s: AccessKeyVendor = %q, want %q", c.p.Chat, got, c.vendor)
		}
		if takes := TakesBailianAccessKey(c.p); takes != (c.vendor == "bailian") {
			t.Errorf("%s: TakesBailianAccessKey = %v", c.p.Chat, takes)
		}
	}
	p, err := FromPreset("bailian-token-plan")
	if err != nil {
		t.Fatal(err)
	}
	if !TakesBailianAccessKey(p) {
		t.Error("the Bailian Token Plan preset doesn't take an access key")
	}
	if q, err := FromPreset("qwen-token-plan"); err != nil || TakesBailianAccessKey(q) {
		t.Error("the Qwen Token Plan preset takes one")
	}
}

// bailianEnvelope is the gateway's reply around a business payload:
// data.DataV2.data.data, as the CLI unwraps it (unwrapResponse,
// packages/core/src/console/models.ts). It carries the success marks a
// real answer carries, so every read here is checked at each level: the
// root's code 200, and SUCCESS on the console's own levels, as the main
// agent read them from the real answer (bailianSuccessCode).
func bailianEnvelope(payload string) string {
	return `{"success":true,"code":200,"data":{"success":true,"DataV2":{"success":true,"code":"SUCCESS","data":{"success":true,"code":"SUCCESS","data":` + payload + `}}}}`
}

// Personal-plan response shapes with synthetic usage and timestamps.
// The public Pro quota is retained; no account data is included.
const (
	bailianRealSubscription = `{"specCode":"pro","remainingDays":5,"startTime":2000000000000,"endTime":2002592000000,"autoRenewFlag":false,"status":"VALID"}`
	bailianRealQuotaConfig  = `{"pro":{"five_hour":12000,"monthly":180000}}`
	bailianRealUsage        = `{"per1MonthPercentage":0.25,"per1MonthResetTime":2002592000000}`
)

type bailianVendor struct {
	mu     sync.Mutex
	token  int // GenerateCLIAccessToken calls
	asked  int // console gateway calls
	secret map[string]string
	// answer is the console API's payload, by access key and API suffix;
	// nil means the real replies above
	payload func(ak, api string) (string, bool)
	// tokenFails answers GenerateCLIAccessToken with a refusal
	tokenFails func(ak string) bool
	// notLogined answers a console call with the gateway's NotLogined
	notLogined func(ak, api string) bool
	// down answers a console call 503, a vendor's failing while
	down func(ak, api string) bool
	srv  *httptest.Server
}

func newBailianVendor(t *testing.T, secrets map[string]string) *bailianVendor {
	t.Helper()
	v := &bailianVendor{secret: secrets}
	v.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Header.Get("X-Host")
		switch host {
		case bailianTokenAPIHost:
			v.tokenServed(t, w, r)
		case bailianConsoleHost:
			v.consoleServed(t, w, r)
		default:
			t.Errorf("asked %s%s", host, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(v.srv.Close)
	old := http.DefaultClient.Transport
	http.DefaultClient.Transport = rewrite{v.srv}
	t.Cleanup(func() { http.DefaultClient.Transport = old })
	return v
}

// tokenServed checks the token request is the signed empty POST the CLI
// sends, and answers the CLI's reply.
func (v *bailianVendor) tokenServed(t *testing.T, w http.ResponseWriter, r *http.Request) {
	t.Helper()
	if r.Method != http.MethodPost || r.URL.Path != bailianTokenAPIPath || r.URL.RawQuery != "" {
		t.Errorf("token: %s %s?%s", r.Method, r.URL.Path, r.URL.RawQuery)
	}
	body, _ := io.ReadAll(r.Body)
	if len(body) != 0 {
		t.Errorf("token: body %q", body)
	}
	auth := r.Header.Get("Authorization")
	cred, _, _ := strings.Cut(strings.TrimPrefix(auth, "ACS3-HMAC-SHA256 Credential="), ",")
	date, err := time.Parse("2006-01-02T15:04:05Z", r.Header.Get("X-Acs-Date"))
	if err != nil {
		t.Errorf("token: date %q", r.Header.Get("X-Acs-Date"))
	}
	nonce := r.Header.Get("X-Acs-Signature-Nonce")
	if len(nonce) < 16 || r.Header.Get("X-Acs-Action") != bailianTokenAction || r.Header.Get("X-Acs-Version") != bailianTokenVersion ||
		r.Header.Get("X-Acs-Content-Sha256") != volcHex(nil) || r.Header.Get("Content-Type") != bailianContentType {
		t.Errorf("token: headers %v", r.Header)
	}
	if want := acs3Sign(cred, v.secret[cred], bailianTokenAPIHost, bailianTokenAPIPath, bailianTokenAction, bailianTokenVersion, nil, date, nonce); auth != want {
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, `{"Success":false,"Code":"SignatureDoesNotMatch","Message":"Specified signature is not matched with our calculation."}`)
		return
	}
	v.mu.Lock()
	v.token++
	v.mu.Unlock()
	if v.tokenFails != nil && v.tokenFails(cred) {
		w.WriteHeader(http.StatusForbidden)
		io.WriteString(w, `{"Success":false,"Code":"InvalidAccessKeyId.NotFound","Message":"Specified access key is not found."}`)
		return
	}
	io.WriteString(w, `{"cliAccessToken":"ct-`+cred+`","Success":true}`)
}

// consoleServed checks the console call is the CLI's form post with the
// token as a Bearer (never the access key or the plan's sk- key), and
// answers like the gateway.
func (v *bailianVendor) consoleServed(t *testing.T, w http.ResponseWriter, r *http.Request) {
	t.Helper()
	if r.Method != http.MethodPost || r.URL.Path != bailianConsolePath {
		t.Errorf("console: %s %s", r.Method, r.URL.Path)
	}
	q := r.URL.Query()
	if q.Get("action") != bailianConsoleAction || q.Get("product") != bailianConsoleProduct {
		t.Errorf("console: query %s", r.URL.RawQuery)
	}
	api := q.Get("api")
	if !strings.HasPrefix(api, "zeldaHttp.apikeyMgr./tokenplan/personal/api/v2/") {
		t.Errorf("console: api %q", api)
	}
	if r.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
		t.Errorf("console: content-type %q", r.Header.Get("Content-Type"))
	}
	if err := r.ParseForm(); err != nil || r.PostForm.Get("region") != bailianConsoleRegion {
		t.Errorf("console: form %v", r.PostForm)
	}
	var params struct {
		Api  string `json:"Api"`
		V    string `json:"V"`
		Data struct {
			CornerstoneParam map[string]any `json:"cornerstoneParam"`
		} `json:"Data"`
	}
	if err := json.Unmarshal([]byte(r.PostForm.Get("params")), &params); err != nil || params.Api != api || params.V != "1.0" {
		t.Errorf("console: params %s", r.PostForm.Get("params"))
	}
	cp := params.Data.CornerstoneParam
	if cp["protocol"] != "V2" || cp["console"] != "ONE_CONSOLE" || cp["productCode"] != "p_efm" || cp["switchUserType"] != float64(3) || cp["consoleSite"] != "BAILIAN_ALIYUN" {
		t.Errorf("console: cornerstoneParam %v", cp)
	}
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ct-") {
		t.Errorf("console: authorization %q", auth)
	}
	if strings.Contains(r.PostForm.Encode(), "SK") {
		t.Errorf("console: the Secret was sent: %s", r.PostForm.Encode())
	}
	v.mu.Lock()
	v.asked++
	v.mu.Unlock()
	ak := strings.TrimPrefix(auth, "Bearer ct-")
	if v.notLogined != nil && v.notLogined(ak, api) {
		io.WriteString(w, `{"data":{"success":false,"errorCode":"NotLogined"}}`)
		return
	}
	if v.down != nil && v.down(ak, api) {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	payload := ""
	switch {
	case v.payload != nil:
		if p, ok := v.payload(ak, api); ok {
			payload = p
		}
	}
	if payload == "" {
		switch {
		case strings.HasSuffix(api, "/subscription"):
			payload = bailianRealSubscription
		case strings.HasSuffix(api, "/quota-config"):
			payload = bailianRealQuotaConfig
		case strings.HasSuffix(api, "/usage"):
			payload = bailianRealUsage
		}
	}
	if strings.HasPrefix(payload, "RAW:") { // an envelope of the test's own
		io.WriteString(w, strings.TrimPrefix(payload, "RAW:"))
		return
	}
	io.WriteString(w, bailianEnvelope(payload))
}

// bailianTest isolates the run and saves the providers as the Usage page
// reads them.
func bailianTest(t *testing.T) {
	t.Helper()
	isolate(t)
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, ".cache"))
	t.Setenv("PATH", h)
	for _, v := range agentenv.Vars {
		t.Setenv(v, "")
	}
	bailianConsoleTokens.Lock()
	bailianConsoleTokens.m = nil
	bailianConsoleTokens.Unlock()
	t.Cleanup(func() {
		bailianConsoleTokens.Lock()
		bailianConsoleTokens.m = nil
		bailianConsoleTokens.Unlock()
	})
}

// bailianCard is the card PlanQuotas read for the provider, with the
// minute's cache set aside when again is true. The console tokens are
// kept: only a changed or cleared key drops them (forgetPlanQuotas).
func bailianCard(t *testing.T, id string, again bool) SubscriptionQuota {
	t.Helper()
	if again {
		planQuotaCache.Lock()
		planQuotaCache.at = time.Time{}
		planQuotaCache.Unlock()
	}
	for _, q := range PlanQuotas(context.Background()) {
		if q.Provider == id {
			return q
		}
	}
	t.Fatalf("no card for %s", id)
	return SubscriptionQuota{}
}

// The personal plan's card: the month's share used (a [0,1] share, not
// points) and, from the spec's quota config, the credits it counts,
// resetting when usage says; the plan's end and that it doesn't renew.
// The plan is the account's: two providers on one access key read one
// card, a token already traded is not traded again, and each credential
// gets its own.
func TestBailianPlanQuotas(t *testing.T) {
	bailianTest(t)
	v := newBailianVendor(t, map[string]string{"LTAIone": "SKone", "LTAItwo": "SKtwo"})
	const root = "https://token-plan.cn-beijing.maas.aliyuncs.com"
	one := Provider{ID: "bailian-token-plan", Name: "Bailian Token Plan", Icon: "bailian-color",
		Chat: root + "/compatible-mode/v1", Anthropic: root + "/apps/anthropic", Key: "sk-sp-plan-key",
		AccessKeyID: "LTAIone", SecretAccessKey: "SKone"}
	if err := Save(one); err != nil {
		t.Fatal(err)
	}
	copy := one
	copy.ID, copy.Name, copy.Key = "bailian-copy", "Bailian copy", "sk-sp-another-key"
	if err := Save(copy); err != nil {
		t.Fatal(err)
	}
	two := one
	two.ID, two.Name, two.Key = "bailian-two", "Bailian two", "sk-sp-two"
	two.AccessKeyID, two.SecretAccessKey = "LTAItwo", "SKtwo"
	if err := Save(two); err != nil {
		t.Fatal(err)
	}

	q := bailianCard(t, "bailian-token-plan", false)
	if q.Plan != "Bailian Token Plan · pro" || q.Error != "" || len(q.Windows) != 1 {
		t.Fatalf("card: %+v", q)
	}
	w := q.Windows[0]
	if w.Name != "Month" || w.Unit != "credits" || w.Used != 0.25*100 ||
		w.Limit != 180000 || w.Amount != 180000*w.Used/100 {
		t.Errorf("window: %+v", w)
	}
	if w.ResetsAt == nil || !w.ResetsAt.Equal(time.UnixMilli(2002592000000)) {
		t.Errorf("reset: %+v", w.ResetsAt)
	}
	if q.Until == nil || !q.Until.Equal(time.UnixMilli(2002592000000)) || q.Renew != "off" {
		t.Errorf("term: %v %q", q.Until, q.Renew)
	}
	// two accounts' access keys, each read once: two trades, six calls
	v.mu.Lock()
	tokens, asked := v.token, v.asked
	v.mu.Unlock()
	if tokens != 2 || asked != 6 {
		t.Errorf("two credentials: %d token trades, %d console calls", tokens, asked)
	}
	// the same access key on another provider: one card, named for the
	// first provider, and the token traded before is used again, never
	// traded again
	copyCard := bailianCard(t, "bailian-token-plan", true)
	if copyCard.Plan != q.Plan || len(copyCard.Windows) != 1 || copyCard.Windows[0].Used != w.Used {
		t.Errorf("the copy: %+v", copyCard)
	}
	for _, card := range PlanQuotas(context.Background()) {
		if strings.HasPrefix(card.Plan, "Bailian Token Plan") && card.Provider != "bailian-token-plan" && card.Provider != "bailian-two" {
			t.Errorf("a second card for the same access key: %+v", card)
		}
	}
	// read once more: still the one trade for those credentials
	bailianCard(t, "bailian-token-plan", true)
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.token != tokens {
		t.Errorf("a kept token was traded again: %d → %d", tokens, v.token)
	}
}

// What each reading tells, apart: nothing used, everything used, no
// share at all, a spec no config lists, no plan, a business error and a
// reply magpie can't read. None of them is turned into 0% or 0 credits.
func TestBailianPlanReadings(t *testing.T) {
	bailianTest(t)
	v := newBailianVendor(t, map[string]string{"LTAIzero": "SK", "LTAIfull": "SK", "LTAIunknown": "SK",
		"LTAInoplan": "SK", "LTAIerr": "SK", "LTAIodd": "SK", "LTAInoshare": "SK"})
	v.payload = func(ak, api string) (string, bool) {
		switch ak + " " + strings.TrimPrefix(api, "zeldaHttp.apikeyMgr./tokenplan/personal/api/v2/") {
		case "LTAIzero usage":
			// nothing used yet, with the month's reset
			return `{"per1MonthPercentage":0,"per1MonthResetTime":2002592000000}`, true
		case "LTAIfull usage":
			return `{"per1MonthPercentage":1,"per1MonthResetTime":2002592000000}`, true
		case "LTAIunknown subscription":
			return `{"specCode":"max","startTime":2000000000000,"endTime":2002592000000,"status":"VALID"}`, true
		case "LTAIunknown quota-config":
			// two specs, neither the subscription's
			return `{"pro":{"five_hour":12000,"monthly":180000},"lite":{"five_hour":2000,"monthly":20000}}`, true
		case "LTAInoplan subscription":
			return `{}`, true
		case "LTAIerr usage":
			return "RAW:" + `{"data":{"success":false,"errorCode":"Forbidden.RAM"}}`, true
		case "LTAIodd usage":
			return "RAW:" + `{"data":{"unexpected":"shape"}}`, true
		case "LTAInoshare usage":
			return `{"per1MonthResetTime":2002592000000}`, true
		}
		return "", false
	}
	const root = "https://token-plan.cn-beijing.maas.aliyuncs.com"
	for _, c := range []struct{ id, ak string }{
		{"bl-zero", "LTAIzero"}, {"bl-full", "LTAIfull"}, {"bl-unknown", "LTAIunknown"},
		{"bl-noplan", "LTAInoplan"}, {"bl-err", "LTAIerr"}, {"bl-odd", "LTAIodd"}, {"bl-noshare", "LTAInoshare"},
	} {
		p := Provider{ID: c.id, Name: c.id, Chat: root + "/compatible-mode/v1", Key: "sk-sp-" + c.id,
			AccessKeyID: c.ak, SecretAccessKey: "SK"}
		if err := Save(p); err != nil {
			t.Fatal(err)
		}
	}

	zero := bailianCard(t, "bl-zero", false)
	if zero.Error != "" || len(zero.Windows) != 1 || zero.Windows[0].Used != 0 ||
		zero.Windows[0].Limit != 180000 || zero.Windows[0].Amount != 0 || zero.Windows[0].ResetsAt == nil {
		t.Errorf("nothing used: %+v", zero)
	}
	full := bailianCard(t, "bl-full", false)
	if full.Error != "" || len(full.Windows) != 1 || full.Windows[0].Used != 100 ||
		full.Windows[0].Amount != full.Windows[0].Limit {
		t.Errorf("everything used: %+v", full)
	}
	unknown := bailianCard(t, "bl-unknown", false)
	if unknown.Error != "" || unknown.Plan != "Bailian Token Plan · max" || len(unknown.Windows) != 1 ||
		unknown.Windows[0].Limit != 0 || unknown.Windows[0].Amount != 0 || unknown.Windows[0].Used != 0.25*100 {
		t.Errorf("an unknown spec keeps its share without a count: %+v", unknown)
	}
	noplan := bailianCard(t, "bl-noplan", false)
	if !strings.Contains(noplan.Error, "no Bailian Token Plan personal") || len(noplan.Windows) != 0 {
		t.Errorf("no plan: %+v", noplan)
	}
	errCard := bailianCard(t, "bl-err", false)
	if !strings.Contains(errCard.Error, "Forbidden.RAM") {
		t.Errorf("business error: %+v", errCard)
	}
	odd := bailianCard(t, "bl-odd", false)
	if !strings.Contains(odd.Error, "can't read") {
		t.Errorf("an envelope magpie can't read: %+v", odd)
	}
	noshare := bailianCard(t, "bl-noshare", false)
	if !strings.Contains(noshare.Error, "no share of the month") || len(noshare.Windows) != 0 {
		t.Errorf("a share missing is no 0%%: %+v", noshare)
	}
}

// A console token the gateway calls NotLogined is traded once more and
// the call tried once more, never more; a refused access key says what
// to check, and neither the Secret nor a token is ever in an error.
func TestBailianTokenRetryAndRefusals(t *testing.T) {
	const root = "https://token-plan.cn-beijing.maas.aliyuncs.com"
	save := func(t *testing.T, id, ak, sk string) {
		t.Helper()
		p := Provider{ID: id, Name: id, Chat: root + "/compatible-mode/v1", Key: "sk-sp-" + id,
			AccessKeyID: ak, SecretAccessKey: sk}
		if err := Save(p); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("aged out once", func(t *testing.T) {
		bailianTest(t)
		v := newBailianVendor(t, map[string]string{"LTAIexpired": "SKexpired"})
		var once atomic.Bool
		v.notLogined = func(ak, api string) bool { return once.CompareAndSwap(false, true) }
		save(t, "bl-expired", "LTAIexpired", "SKexpired")
		if q := bailianCard(t, "bl-expired", false); q.Error != "" || len(q.Windows) != 1 {
			t.Errorf("a token aged out, traded again: %+v", q)
		}
		v.mu.Lock()
		defer v.mu.Unlock()
		if v.token != 2 {
			t.Errorf("a NotLogined traded %d tokens, want 2", v.token)
		}
	})

	t.Run("turned away again", func(t *testing.T) {
		bailianTest(t)
		v := newBailianVendor(t, map[string]string{"LTAIdead": "SKdead"})
		v.notLogined = func(ak, api string) bool { return true }
		save(t, "bl-dead", "LTAIdead", "SKdead")
		q := bailianCard(t, "bl-dead", false)
		if !strings.Contains(q.Error, "not logged in") {
			t.Errorf("a token refused again: %+v", q)
		}
		v.mu.Lock()
		defer v.mu.Unlock()
		if v.token != 2 {
			t.Errorf("a refused-again read traded %d tokens, want 2", v.token)
		}
		for _, secret := range []string{"SKdead", "ct-"} {
			if strings.Contains(q.Error, secret) {
				t.Errorf("the error tells %q: %s", secret, q.Error)
			}
		}
	})

	t.Run("access key refused", func(t *testing.T) {
		bailianTest(t)
		v := newBailianVendor(t, map[string]string{"LTAIrefused": "SKrefused"})
		v.tokenFails = func(ak string) bool { return true }
		save(t, "bl-refused", "LTAIrefused", "SKrefused")
		q := bailianCard(t, "bl-refused", false)
		if !strings.Contains(q.Error, "InvalidAccessKeyId.NotFound") || !strings.Contains(q.Error, "AccessKey ID and Secret") {
			t.Errorf("a refused access key: %+v", q)
		}
		for _, secret := range []string{"SKrefused", "ct-"} {
			if strings.Contains(q.Error, secret) {
				t.Errorf("the error tells %q: %s", secret, q.Error)
			}
		}
	})
}

// A read that fails in passing keeps the card it read before, as other
// plans' cards do.
func TestBailianPlanKeepsLastReading(t *testing.T) {
	bailianTest(t)
	v := newBailianVendor(t, map[string]string{"LTAIkept": "SKkept"})
	const root = "https://token-plan.cn-beijing.maas.aliyuncs.com"
	p := Provider{ID: "bl-kept", Name: "Bailian kept", Chat: root + "/compatible-mode/v1", Key: "sk-sp-kept",
		AccessKeyID: "LTAIkept", SecretAccessKey: "SKkept"}
	if err := Save(p); err != nil {
		t.Fatal(err)
	}
	good := bailianCard(t, "bl-kept", false)
	if good.Error != "" || len(good.Windows) != 1 {
		t.Fatalf("first read: %+v", good)
	}
	v.down = func(ak, api string) bool { return true }
	// a token already stored survives, so the failure is the console call,
	// and the card read before is kept through it
	later := bailianCard(t, "bl-kept", true)
	if len(later.Windows) != 1 || later.Windows[0].Used != good.Windows[0].Used || later.AsOf == nil {
		t.Errorf("the last reading was not kept: %+v", later)
	}
}

// A read given up on says so and stops: nothing waits on the vendor past
// the caller's context.
func TestBailianPlanContextCanceled(t *testing.T) {
	bailianTest(t)
	held := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-held
	}))
	defer srv.Close()
	defer close(held)
	old := http.DefaultClient.Transport
	http.DefaultClient.Transport = rewrite{srv}
	defer func() { http.DefaultClient.Transport = old }()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan error, 1)
	go func() {
		_, err := bailianConsole(ctx, "LTAIone", "SKone", bailianUsageAPI)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "context canceled") {
			t.Errorf("canceled: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("a canceled read waited on the vendor")
	}
}

// bailianProviderOf is a provider of Bailian's personal plan with the
// access key given, for a read that needn't be saved.
func bailianProviderOf(id, ak, sk string) Provider {
	return Provider{ID: id, Name: id, Chat: "https://token-plan.cn-beijing.maas.aliyuncs.com/compatible-mode/v1",
		Key: "sk-sp-" + id, AccessKeyID: ak, SecretAccessKey: sk}
}

// The credits a card counts are the subscription's own spec's, never
// another's: a spec the config doesn't list keeps its share and reset
// without a total, and a subscription that named no spec gets none
// either — even when the config lists exactly one, as the real reply
// does.
func TestBailianUnknownSpecHasNoCredits(t *testing.T) {
	bailianTest(t)
	v := newBailianVendor(t, map[string]string{"LTAIunknown": "SK", "LTAIempty": "SK"})
	v.payload = func(ak, api string) (string, bool) {
		switch {
		case strings.HasSuffix(api, "/subscription") && ak == "LTAIunknown":
			return `{"specCode":"unknown-tier","status":"VALID"}`, true
		case strings.HasSuffix(api, "/subscription") && ak == "LTAIempty":
			return `{"status":"VALID","endTime":2002592000000}`, true
		case strings.HasSuffix(api, "/quota-config"):
			// the real reply: pro alone, 180,000 credits a month
			return bailianRealQuotaConfig, true
		}
		return "", false
	}
	for _, id := range []string{"bl-unknown-real", "bl-nospec"} {
		p := bailianProviderOf(id, map[string]string{"bl-unknown-real": "LTAIunknown", "bl-nospec": "LTAIempty"}[id], "SK")
		if err := Save(p); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"bl-unknown-real", "bl-nospec"} {
		q := bailianCard(t, id, false)
		if q.Error != "" || len(q.Windows) != 1 {
			t.Fatalf("%s: %+v", id, q)
		}
		w := q.Windows[0]
		if w.Limit != 0 || w.Amount != 0 || w.Used != 0.25*100 {
			t.Errorf("%s was given another spec's credits: %+v", id, w)
		}
	}
}

// An answer that says it failed at any level is a failure, even at HTTP
// 200 and with the business payload still in it; a code is read even
// where success isn't given.
func TestBailianEnvelopeFailures(t *testing.T) {
	for name, raw := range map[string]string{
		"root success false":  `{"success":false,"code":"Forbidden.RAM","data":{"DataV2":{"data":{"data":` + bailianRealUsage + `}}}}`,
		"root code":           `{"code":"Forbidden.RAM","data":{"DataV2":{"data":{"data":` + bailianRealUsage + `}}}}`,
		"gateway errorCode":   `{"data":{"errorCode":"Forbidden.RAM","DataV2":{"data":{"data":` + bailianRealUsage + `}}}}`,
		"DataV2 code":         `{"data":{"DataV2":{"code":"Forbidden.RAM","data":{"data":` + bailianRealUsage + `}}}}`,
		"business level code": `{"data":{"DataV2":{"data":{"code":"Forbidden.RAM","data":` + bailianRealUsage + `}}}}`,
		"no reason given":     `{"success":false,"data":{"DataV2":{"data":{"data":` + bailianRealUsage + `}}}}`,
		// a flag that is there but says nothing magpie can read is no
		// success: 0 and the like are not guessed at, nor taken for the
		// field being absent
		"success 0":      `{"success":0,"data":{"DataV2":{"data":{"data":` + bailianRealUsage + `}}}}`,
		"success object": `{"success":{"value":false},"data":{"DataV2":{"data":{"data":` + bailianRealUsage + `}}}}`,
		"success array":  `{"success":[],"data":{"DataV2":{"data":{"data":` + bailianRealUsage + `}}}}`,
		"success null":   `{"success":null,"data":{"DataV2":{"data":{"data":` + bailianRealUsage + `}}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			bailianTest(t)
			v := newBailianVendor(t, map[string]string{"LTAIreview": "SK"})
			v.payload = func(ak, api string) (string, bool) {
				if strings.HasSuffix(api, "/usage") {
					return "RAW:" + raw, true
				}
				return "", false
			}
			if q := bailianPlanQuota(context.Background(), bailianProviderOf("bl-envelope", "LTAIreview", "SK")); q.Error == "" {
				t.Errorf("a failed answer was read as a quota: %+v", q.Windows)
			}
		})
	}
}

// The marks of a successful answer are taken for success, not for the
// errors they are written like: the root's 200 and the console's
// SUCCESS, a number or a string, and an answer that gives no code.
func TestBailianEnvelopeSuccessCodes(t *testing.T) {
	for name, raw := range map[string]string{
		"number 200":   `{"success":true,"code":200,"data":{"success":true,"DataV2":{"success":true,"code":"SUCCESS","data":{"success":true,"code":"SUCCESS","data":` + bailianRealUsage + `}}}}`,
		"string 200":   `{"code":"200","data":{"DataV2":{"code":"SUCCESS","data":{"data":` + bailianRealUsage + `}}}}`,
		"success only": `{"data":{"success":true,"DataV2":{"data":{"success":true,"data":` + bailianRealUsage + `}}}}`,
		"no marks":     `{"data":{"DataV2":{"data":{"data":` + bailianRealUsage + `}}}}`,
		"worded":       `{"success":"true","code":"SUCCESS","data":{"success":"true","DataV2":{"data":{"data":` + bailianRealUsage + `}}}}`,
		"capitals":     `{"success":"TRUE","code":"200","data":{"DataV2":{"data":{"data":` + bailianRealUsage + `}}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			bailianTest(t)
			v := newBailianVendor(t, map[string]string{"LTAIreview": "SK"})
			v.payload = func(ak, api string) (string, bool) {
				if strings.HasSuffix(api, "/usage") {
					return "RAW:" + raw, true
				}
				return "", false
			}
			q := bailianPlanQuota(context.Background(), bailianProviderOf("bl-envelope-ok", "LTAIreview", "SK"))
			if q.Error != "" || len(q.Windows) != 1 || q.Windows[0].Used != 0.25*100 {
				t.Errorf("a successful answer was refused: %+v", q)
			}
		})
	}
}

// A plan that has ended and won't renew has no month starting again: a
// read that fails in passing keeps its window as it was read, not at 0%
// with the count it had.
func TestBailianExpiredPlanKeepsItsReading(t *testing.T) {
	bailianTest(t)
	v := newBailianVendor(t, map[string]string{"LTAIended": "SK"})
	ended := time.Now().Add(-time.Hour).UnixMilli()
	v.payload = func(ak, api string) (string, bool) {
		switch {
		case strings.HasSuffix(api, "/subscription"):
			return fmt.Sprintf(`{"specCode":"pro","endTime":%d,"autoRenewFlag":false,"status":"VALID"}`, ended), true
		case strings.HasSuffix(api, "/usage"):
			return fmt.Sprintf(`{"per1MonthPercentage":0.56,"per1MonthResetTime":%d}`, ended), true
		}
		return "", false
	}
	p := bailianProviderOf("bl-ended", "LTAIended", "SK")
	if err := Save(p); err != nil {
		t.Fatal(err)
	}
	good := bailianCard(t, p.ID, false)
	if good.Error != "" || len(good.Windows) != 1 || math.Abs(good.Windows[0].Used-56) > 1e-9 {
		t.Fatalf("first read: %+v", good)
	}
	v.down = func(ak, api string) bool { return true }
	later := bailianCard(t, p.ID, true)
	if later.AsOf == nil || len(later.Windows) != 1 {
		t.Fatalf("no reading kept: %+v", later)
	}
	if later.Windows[0].Used != good.Windows[0].Used || later.Windows[0].ResetsAt == nil ||
		later.Windows[0].Amount != good.Windows[0].Amount || later.Windows[0].Limit != good.Windows[0].Limit {
		t.Errorf("an ended, non-renewing plan was made unused: before=%+v after=%+v", good.Windows[0], later.Windows[0])
	}
	if later.Renew != "off" || later.Until == nil {
		t.Errorf("the plan's end was lost: %v %q", later.Until, later.Renew)
	}
}

// quota-config failing is an error, never a younger reading with the
// credits taken out: a failure in passing keeps the last complete one,
// while an unknown spec read well keeps its share without a count.
func TestBailianQuotaConfigFailures(t *testing.T) {
	t.Run("failing", func(t *testing.T) {
		bailianTest(t)
		v := newBailianVendor(t, map[string]string{"LTAIcfg": "SK"})
		p := bailianProviderOf("bl-cfg", "LTAIcfg", "SK")
		if err := Save(p); err != nil {
			t.Fatal(err)
		}
		good := bailianCard(t, p.ID, false)
		if good.Error != "" || len(good.Windows) != 1 || good.Windows[0].Limit != 180000 {
			t.Fatalf("first read: %+v", good)
		}
		v.down = func(ak, api string) bool { return strings.HasSuffix(api, "/quota-config") }
		later := bailianCard(t, p.ID, true)
		if later.AsOf == nil || len(later.Windows) != 1 || later.Windows[0].Limit != good.Windows[0].Limit ||
			later.Windows[0].Amount != good.Windows[0].Amount {
			t.Errorf("a failed quota-config replaced the complete reading: %+v", later)
		}
	})
	for name, answer := range map[string]string{
		"business error": `RAW:{"data":{"success":false,"errorCode":"Forbidden.RAM"}}`,
		"unreadable":     `RAW:{"data":{"DataV2":{"data":{"data":42}}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			bailianTest(t)
			v := newBailianVendor(t, map[string]string{"LTAIcfg": "SK"})
			v.payload = func(ak, api string) (string, bool) {
				if strings.HasSuffix(api, "/quota-config") {
					return answer, true
				}
				return "", false
			}
			q := bailianPlanQuota(context.Background(), bailianProviderOf("bl-cfg", "LTAIcfg", "SK"))
			if q.Error == "" || len(q.Windows) != 0 {
				t.Errorf("a failed quota-config was read as a quota: %+v", q)
			}
		})
	}
}

// A vendor's answer can echo what must not be shown: the errors magpie
// gives say what happened without the Secret, the AccessKey ID or the
// console token (synthetic values here, nothing of an account's).
func TestBailianErrorsKeepNoSecrets(t *testing.T) {
	const token, secret, ak = "ct-synthetic-token-sentinel", "SK-synthetic-secret-sentinel", "LTAIsynthetic"
	for _, c := range []struct {
		name   string
		status int
		body   string
		token  bool // the token endpoint's answer, not the console's
	}{
		{"console status", http.StatusUnauthorized, `{"message":"session rejected","cliAccessToken":"` + token + `"}`, false},
		{"console errorCode", 0, `{"data":{"success":false,"errorCode":"session ` + token + ` rejected"}}`, false},
		{"token message", http.StatusForbidden, `{"Code":"SignatureDoesNotMatch","Message":"diagnostic ` + secret + `"}`, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			bailianTest(t)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if c.status != 0 {
					w.WriteHeader(c.status)
				}
				io.WriteString(w, c.body)
			}))
			defer srv.Close()
			old := http.DefaultClient.Transport
			http.DefaultClient.Transport = rewrite{srv}
			defer func() { http.DefaultClient.Transport = old }()
			var err error
			if c.token {
				_, err = bailianGenerateToken(context.Background(), ak, secret)
			} else {
				_, err = bailianConsoleCall(context.Background(), token, bailianUsageAPI)
			}
			if err == nil {
				t.Fatal("a failed read said nothing")
			}
			for _, s := range []string{token, secret, ak} {
				if strings.Contains(err.Error(), s) {
					t.Errorf("the error tells %q: %s", s, err)
				}
			}
		})
	}
}

// Clearing an access key drops the console token traded for it, and the
// next read trades again rather than reading with what was.
func TestBailianForgetDropsConsoleTokens(t *testing.T) {
	bailianTest(t)
	v := newBailianVendor(t, map[string]string{"LTAIone": "SK"})
	p := bailianProviderOf("bl-forget", "LTAIone", "SK")
	if err := Save(p); err != nil {
		t.Fatal(err)
	}
	if q := bailianCard(t, p.ID, false); q.Error != "" {
		t.Fatalf("first read: %+v", q)
	}
	ForgetPlanQuotas()
	bailianConsoleTokens.Lock()
	retained := len(bailianConsoleTokens.m)
	bailianConsoleTokens.Unlock()
	if retained != 0 {
		t.Fatalf("%d console token(s) survived the forget", retained)
	}
	if q := bailianCard(t, p.ID, true); q.Error != "" {
		t.Fatalf("the read after the forget: %+v", q)
	}
	v.mu.Lock()
	trades := v.token
	v.mu.Unlock()
	if trades != 2 {
		t.Errorf("the read after the forget traded %d times, want 2", trades)
	}
}

// A read that was under way when its provider's access key was cleared
// says nothing of what is: it publishes no card, no history and no
// kept reading, so the next read is the one without a key.
func TestBailianClearWhileReadingIsNotRepublished(t *testing.T) {
	bailianTest(t)
	v := newBailianVendor(t, map[string]string{"LTAIinflight": "SK"})
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	v.payload = func(ak, api string) (string, bool) {
		if strings.HasSuffix(api, "/usage") {
			once.Do(func() { close(entered) })
			<-release
		}
		return "", false
	}
	p := bailianProviderOf("bl-inflight", "LTAIinflight", "SK")
	if err := Save(p); err != nil {
		t.Fatal(err)
	}
	done := make(chan []SubscriptionQuota, 1)
	go func() { done <- PlanQuotas(context.Background()) }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("the read did not start")
	}
	p.AccessKeyID, p.SecretAccessKey = "", ""
	saveErr := Save(p)
	ForgetPlanQuotas()
	close(release)
	if saveErr != nil {
		t.Fatal(saveErr)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the read did not finish")
	}
	stored, err := Find(p.ID)
	if err != nil || stored.AccessKeyID != "" {
		t.Fatalf("the clear was not stored: %v", err)
	}
	got := PlanQuotas(context.Background())
	if len(got) != 1 || !got[0].NeedsAccessKey || len(got[0].Windows) != 0 {
		t.Errorf("a read begun before the clear republished its card: %+v", got)
	}
}

// A token traded by a read that began before the access key was cleared
// is that read's alone: the clear leaves nothing of the old key behind,
// not even what the read was still trading.
func TestBailianTokenTradedBeforeClearIsNotKept(t *testing.T) {
	bailianTest(t)
	newBailianVendor(t, map[string]string{"LTAIone": "SK"})
	begun, _ := quotaReading(context.Background())
	forgetPlanQuotas()
	if _, err := bailianToken(begun, "LTAIone", "SK", false); err != nil {
		t.Fatal(err)
	}
	bailianConsoleTokens.Lock()
	retained := len(bailianConsoleTokens.m)
	bailianConsoleTokens.Unlock()
	if retained != 0 {
		t.Fatalf("%d token(s) of a read begun before the clear were kept", retained)
	}
	if _, err := bailianToken(context.Background(), "LTAIone", "SK", false); err != nil {
		t.Fatal(err)
	}
	bailianConsoleTokens.Lock()
	retained = len(bailianConsoleTokens.m)
	bailianConsoleTokens.Unlock()
	if retained != 1 {
		t.Fatalf("%d token(s) kept for a read begun after the clear, want 1", retained)
	}
}

// Another Secret is another credential: the token traded for the first
// is not read with for the second.
func TestBailianChangedSecretTradesAgain(t *testing.T) {
	bailianTest(t)
	v := newBailianVendor(t, map[string]string{"LTAIone": "SKold"})
	if _, err := bailianToken(context.Background(), "LTAIone", "SKold", false); err != nil {
		t.Fatal(err)
	}
	v.secret["LTAIone"] = "SKnew"
	if _, err := bailianToken(context.Background(), "LTAIone", "SKnew", false); err != nil {
		t.Fatal(err)
	}
	v.mu.Lock()
	trades := v.token
	v.mu.Unlock()
	if trades != 2 {
		t.Errorf("a changed Secret traded %d times, want 2", trades)
	}
}

// bailianAwaitKeepLastLock waits until a reading is parked on the last
// reading's write, which says keepReading's own check already passed.
func bailianAwaitKeepLastLock(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	buf := make([]byte, 1<<20)
	for time.Now().Before(deadline) {
		n := runtime.Stack(buf, true)
		for _, g := range strings.Split(string(buf[:n]), "\n\n") {
			if strings.Contains(g, ".keepLast(") && strings.Contains(g, ".keepReading(") &&
				(strings.Contains(g, "sync.runtime_SemacquireMutex") || strings.Contains(g, "internal/sync.(*Mutex).lockSlow")) {
				return
			}
		}
		runtime.Gosched()
	}
	t.Fatal("the reading did not park on the last reading's write")
}

// A reading held up at its last reading's write when the access key is
// cleared writes nothing once it is let go: the generation is checked in
// the same window as the write (quotaPublication), not before a wait the
// clear can come through.
func TestBailianKeepReadingAfterClearIsNotWritten(t *testing.T) {
	bailianTest(t)
	ctx, _ := quotaReading(context.Background())
	const tag = "bailian personal cleared-key"
	q := SubscriptionQuota{Provider: "bl-cleared-kept", Name: "Bailian",
		Windows: []QuotaWindow{{Name: "Month", Used: 56, Limit: 180000, Amount: 100800}}}
	lastQuotas.Lock()
	lastQuotas.m, lastQuotas.loaded = nil, true
	held := true
	done := make(chan struct{})
	defer func() {
		if held {
			lastQuotas.Unlock()
		}
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("the reading did not finish")
		}
		lastQuotas.Lock()
		lastQuotas.m, lastQuotas.loaded = nil, false
		lastQuotas.Unlock()
	}()
	go func() { defer close(done); keepReading(ctx, q, tag) }()
	bailianAwaitKeepLastLock(t)
	ForgetPlanQuotas()
	lastQuotas.Unlock()
	held = false
	<-done
	lastQuotas.Lock()
	kept := len(lastQuotas.m)
	lastQuotas.Unlock()
	raw, err := os.ReadFile(lastQuotasPath())
	onDisk := err == nil && strings.Contains(string(raw), q.Provider)
	if kept != 0 || onDisk {
		t.Errorf("a reading begun before the clear was written after it: kept=%d, quotas.json has it=%v", kept, onDisk)
	}
}

// A read that has passed its check but is still reading the history file
// when the access key is cleared publishes neither the history point nor
// the card: the clear waits on neither read, and both commits are dropped.
func TestBailianPlanHistoryAfterClearPublishesNothing(t *testing.T) {
	bailianTest(t)
	newBailianVendor(t, map[string]string{"LTAIheld": "SK"})
	p := bailianProviderOf("bl-cleared-toc", "LTAIheld", "SK")
	if err := Save(p); err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	var enterOnce, releaseOnce sync.Once
	oldRead := readQuotaHist
	readQuotaHist = func() quotaHist {
		enterOnce.Do(func() { close(entered) })
		<-release
		return oldRead()
	}
	defer func() {
		releaseOnce.Do(func() { close(release) })
		readQuotaHist = oldRead
	}()
	done := make(chan []SubscriptionQuota, 1)
	go func() { done <- PlanQuotas(context.Background()) }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		releaseOnce.Do(func() { close(release) })
		<-done
		t.Fatal("the history file was not read")
	}
	p.AccessKeyID, p.SecretAccessKey = "", ""
	saveErr := Save(p)
	ForgetPlanQuotas()
	releaseOnce.Do(func() { close(release) })
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the read did not finish")
	}
	if saveErr != nil {
		t.Fatal(saveErr)
	}
	planQuotaCache.Lock()
	cards := len(planQuotaCache.data)
	planQuotaCache.Unlock()
	raw, err := os.ReadFile(quotaHistPath())
	inHistory := err == nil && strings.Contains(string(raw), p.ID)
	next := bailianCard(t, p.ID, false)
	if cards != 0 || inHistory || len(next.Windows) != 0 || !next.NeedsAccessKey {
		t.Errorf("a read begun before the clear published after it: cache cards=%d, history has it=%v, next windows=%d, asks for a key=%v",
			cards, inHistory, len(next.Windows), next.NeedsAccessKey)
	}
}

// The console token and the Secret are redacted whole, before a
// diagnostic is cut short: a long opaque token's prefix is not what a
// card shows. The values are synthetic.
func TestBailianLongSecretsKeepNoPrefix(t *testing.T) {
	longSecret := "SK-" + strings.Repeat("syntheticsecret", 24)
	longToken := "eyJhbGciOiJSUzI1NiJ9." + strings.Repeat("QUJDREVGR0hJSktMTU5PUFFSU1RVVldYWVo", 12) + ".signature"
	if len(longSecret) <= 300 || len(longToken) <= 300 {
		t.Fatalf("the values are not longer than the diagnostic head: %d, %d", len(longSecret), len(longToken))
	}
	t.Run("console token in the body", func(t *testing.T) {
		bailianTest(t)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
			json.NewEncoder(w).Encode(map[string]string{"cliAccessToken": longToken, "message": "session rejected"})
		}))
		defer srv.Close()
		old := http.DefaultClient.Transport
		http.DefaultClient.Transport = rewrite{srv}
		defer func() { http.DefaultClient.Transport = old }()
		_, err := bailianConsoleCall(context.Background(), longToken, bailianUsageAPI, "LTAIsynthetic", longSecret)
		if err == nil {
			t.Fatal("a rejected session said nothing")
		}
		if strings.Contains(err.Error(), longToken[:48]) {
			t.Errorf("a long console token's prefix survived the cut: %s", err)
		}
	})
	t.Run("secret in the token message", func(t *testing.T) {
		bailianTest(t)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusForbidden)
			json.NewEncoder(w).Encode(map[string]string{"Code": "SignatureDoesNotMatch", "Message": "diagnostic " + longSecret})
		}))
		defer srv.Close()
		old := http.DefaultClient.Transport
		http.DefaultClient.Transport = rewrite{srv}
		defer func() { http.DefaultClient.Transport = old }()
		_, err := bailianGenerateToken(context.Background(), "LTAIsynthetic", longSecret)
		if err == nil {
			t.Fatal("a refused key said nothing")
		}
		if strings.Contains(err.Error(), longSecret[:48]) {
			t.Errorf("a long Secret's prefix survived the cut: %s", err)
		}
	})
}

// The editor is told whose access key a provider takes, and the Secret
// never comes back: /api/providers is read by internal/gui's
// TestProviderListTellsBailianAccessKey.
