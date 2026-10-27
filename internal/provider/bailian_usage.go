package provider

// Bailian's Token Plan 个人版 (#1506) tells what the personal plan has
// used of its month only to the console Bailian's own pages read: three
// APIs behind the CLI console gateway, reached with a short-lived console
// token that Alibaba Cloud's own CLI (modelstudioai/cli) trades for the
// account's AccessKey ID and Secret (kept as AccessKeyID,
// SecretAccessKey), never with the plan's sk- key. The plan a provider
// is on is its endpoint's: token-plan.cn-beijing.maas.aliyuncs.com is
// the personal plan on Alibaba Cloud. The Qwen AI platform's token plan
// (token-plan.maas.qianwenaiapi.com) is another vendor's and reads
// nothing here, and neither has pay-as-you-go windows.
//
// The token is asked of modelstudio.cn-beijing.aliyuncs.com
// (GenerateCLIAccessToken, version 2026-02-10) with an ACS3-HMAC-SHA256
// signature (Alibaba Cloud's V3 signature) and kept in memory alone, by
// the credentials it was traded for: either changing drops it. The
// gateway is bailian-cs.console.aliyun.com/cli/api.json (action
// BroadScopeAspnGateway, product sfm_bailian), which takes the token as
// a Bearer and answers an HTTP 200 that can still say the call failed:
// data.success false with an errorCode, NotLogined when the token has
// aged out, which one fresh token and one retry mends, never more. The
// AccessKey ID and Secret are sent nowhere but
// modelstudio.cn-beijing.aliyuncs.com; the gateway host is fixed, never
// a provider's own URL.
//
// What the APIs tell (read from the real replies of 2026-10-27; the
// tests hold them sanitized):
//
//   - subscription: the plan's specCode, status, startTime/endTime
//     (epoch ms) and autoRenewFlag — the card's Until and Renew. A plan
//     not renewed says so; nothing here promises the credits come back
//     at its end.
//   - quota-config: each spec's monthly and five_hour credits. monthly
//     is the plan's own month; five_hour is a rate limit, not an
//     allowance the account holds, so it makes no window. Add-on
//     packages and harness benefits are other purchases and are not
//     counted in it.
//   - usage: per1MonthPercentage, the share of the month used in [0,1]
//     (not points), and per1MonthResetTime (epoch ms).
//
// A spec the config doesn't list keeps its share and reset without an
// absolute credits count, and nothing a read left unknown becomes 0.

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

const (
	bailianTokenPlanHost = "token-plan.cn-beijing.maas.aliyuncs.com"
	bailianPlanName      = "Bailian Token Plan"

	// the AccessKey ID and Secret are sent to this host alone
	bailianTokenAPIHost = "modelstudio.cn-beijing.aliyuncs.com"
	bailianTokenAPIPath = "/modelstudio/cli/generateAccessToken"
	bailianTokenAction  = "GenerateCLIAccessToken"
	bailianTokenVersion = "2026-02-10"
	bailianContentType  = "application/json"

	// the console gateway, which the console token alone is sent to
	bailianConsoleHost    = "bailian-cs.console.aliyun.com"
	bailianConsolePath    = "/cli/api.json"
	bailianConsoleAction  = "BroadScopeAspnGateway"
	bailianConsoleProduct = "sfm_bailian"
	bailianConsoleRegion  = "cn-beijing"

	bailianAKSKHint = "check the AccessKey ID and Secret in the provider's editor (Alibaba Cloud RAM console → AccessKeys), and that the key belongs to the account the plan is subscribed on"
)

// the console APIs the personal plan is read through, as the CLI names
// them (packages/commands/src/commands/usage/token-plan.ts)
const (
	bailianSubscriptionAPI = "zeldaHttp.apikeyMgr./tokenplan/personal/api/v2/subscription"
	bailianQuotaConfigAPI  = "zeldaHttp.apikeyMgr./tokenplan/personal/api/v2/quota-config"
	bailianUsageAPI        = "zeldaHttp.apikeyMgr./tokenplan/personal/api/v2/usage"
)

// TakesBailianAccessKey says p is on Bailian's Token Plan 个人版 (at
// token-plan.cn-beijing.maas.aliyuncs.com), whose editor then asks for
// the Alibaba Cloud account's AccessKey ID and Secret.
func TakesBailianAccessKey(p Provider) bool {
	if p.Account != nil {
		return false
	}
	for _, base := range []string{p.Chat, p.Responses, p.Anthropic} {
		if base != "" && hostOf(base) == bailianTokenPlanHost {
			return true
		}
	}
	return false
}

// errBailianNotLogined is the console gateway turning a token away as
// aged out, which one fresh token mends.
var errBailianNotLogined = errors.New("Bailian: the console session is not logged in or has expired; magpie will trade the access key for a new one")

// bailianConsoleTokens are the console tokens traded, in memory alone,
// by the credentials (bailianCredKey): either changing drops what the
// other read. A token has no told expiry, so it is used until the
// gateway calls it NotLogined.
var bailianConsoleTokens struct {
	sync.Mutex
	m map[string]string
}

func bailianCredKey(ak, sk string) string {
	sum := sha256.Sum256([]byte(ak + "\x00" + sk))
	return ak + " " + hex.EncodeToString(sum[:8])
}

// bailianToken is the console token the access key is traded for: the
// one kept for these credentials when fresh is false. The token is
// written nowhere but this map, and no lock is held while it is asked
// for: the gateway's answer, not this cache, is what orders anything.
func bailianToken(ctx context.Context, ak, sk string, fresh bool) (string, error) {
	key := bailianCredKey(ak, sk)
	c := &bailianConsoleTokens
	if !fresh {
		c.Lock()
		t, ok := c.m[key]
		c.Unlock()
		if ok {
			return t, nil
		}
	}
	tok, err := bailianGenerateToken(ctx, ak, sk)
	if err != nil {
		return "", err
	}
	c.Lock()
	if c.m == nil {
		c.m = map[string]string{}
	}
	// a token traded by a read begun before a plan's settings changed is
	// that read's alone: it is not kept, so a clear leaves nothing of the
	// old key behind
	if stillCurrent(ctx) {
		c.m[key] = tok
	}
	c.Unlock()
	return tok, nil
}

// bailianForgetToken drops the token the gateway turned away, but only
// that one: a token read again meanwhile is another's to lose.
func bailianForgetToken(ak, sk, token string) {
	key := bailianCredKey(ak, sk)
	c := &bailianConsoleTokens
	c.Lock()
	if c.m[key] == token {
		delete(c.m, key)
	}
	c.Unlock()
}

// bailianForgetTokens drops every token traded: a save changed or
// cleared an access key, so what was traded for it is no longer this
// account's to read with.
func bailianForgetTokens() {
	c := &bailianConsoleTokens
	c.Lock()
	c.m = nil
	c.Unlock()
}

// bailianNonce is the random string the signature never repeats.
func bailianNonce() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%x", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

// acs3Sign is the Authorization header of a POST to host's path with
// body, signed as Alibaba Cloud's V3 signature makes it with the
// AccessKey ID and Secret, at date, with nonce: Content-Type, Host and
// the x-acs-* headers signed, in their sorted order. TestACS3SignMatches
// pins what the official CLI's signer (modelstudioai/cli
// packages/core/src/client/acs.ts) makes of the same request, which is
// the doc's algorithm (help.aliyun.com/zh/sdk/product-overview/
// v3-request-structure-and-signature).
func acs3Sign(ak, sk, host, path, action, version string, body []byte, date time.Time, nonce string) string {
	xDate := date.UTC().Format("2006-01-02T15:04:05Z")
	sha := volcHex(body)
	headers := map[string]string{
		"content-type":          bailianContentType,
		"host":                  host,
		"x-acs-action":          action,
		"x-acs-content-sha256":  sha,
		"x-acs-date":            xDate,
		"x-acs-signature-nonce": nonce,
		"x-acs-version":         version,
	}
	names := []string{"content-type", "host", "x-acs-action", "x-acs-content-sha256", "x-acs-date", "x-acs-signature-nonce", "x-acs-version"}
	var canon strings.Builder
	for _, k := range names {
		canon.WriteString(k + ":" + headers[k] + "\n")
	}
	signed := strings.Join(names, ";")
	canonical := strings.Join([]string{http.MethodPost, path, "", canon.String(), signed, sha}, "\n")
	stringToSign := "ACS3-HMAC-SHA256\n" + volcHex([]byte(canonical))
	sig := hex.EncodeToString(volcMAC([]byte(sk), stringToSign))
	return "ACS3-HMAC-SHA256 Credential=" + ak + ",SignedHeaders=" + signed + ",Signature=" + sig
}

// bailianGenerateToken trades the AccessKey ID and Secret for a console
// token at modelstudio's CLI endpoint, signed ACS3 with an empty body
// and query. The Secret goes nowhere else, and neither comes back in an
// error.
func bailianGenerateToken(ctx context.Context, ak, sk string) (string, error) {
	date := time.Now()
	nonce := bailianNonce()
	auth := acs3Sign(ak, sk, bailianTokenAPIHost, bailianTokenAPIPath, bailianTokenAction, bailianTokenVersion, nil, date, nonce)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://"+bailianTokenAPIHost+bailianTokenAPIPath, strings.NewReader(""))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", bailianContentType)
	req.Header.Set("X-Acs-Action", bailianTokenAction)
	req.Header.Set("X-Acs-Version", bailianTokenVersion)
	req.Header.Set("X-Acs-Date", date.UTC().Format("2006-01-02T15:04:05Z"))
	req.Header.Set("X-Acs-Signature-Nonce", nonce)
	req.Header.Set("X-Acs-Content-Sha256", volcHex(nil))
	req.Header.Set("Authorization", auth)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("Bailian %s: %w", bailianTokenAction, err)
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return "", fmt.Errorf("Bailian %s: %w", bailianTokenAction, err)
	}
	var env struct {
		Success        *bool  `json:"Success"`
		Code           string `json:"Code"`
		Message        string `json:"Message"`
		CLIAccessToken string `json:"cliAccessToken"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return "", fmt.Errorf("Bailian %s: a reply magpie can't read: %s", bailianTokenAction, shapeOf(raw))
	}
	token := strings.TrimSpace(env.CLIAccessToken)
	switch {
	case res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden:
		return "", fmt.Errorf("Bailian %s: %s; %s", bailianTokenAction, bailianSaid(res.Status, env.Code, env.Message, ak, sk), bailianAKSKHint)
	case res.StatusCode >= 300, env.Success != nil && !*env.Success:
		said := bailianSaid(res.Status, env.Code, env.Message, ak, sk)
		if bailianRefusesKey(env.Code, res.StatusCode) {
			said += "; " + bailianAKSKHint
		}
		return "", fmt.Errorf("Bailian %s: %s", bailianTokenAction, said)
	case token == "":
		return "", fmt.Errorf("Bailian %s: the reply told no cliAccessToken: %s", bailianTokenAction, shapeOf(raw))
	}
	return token, nil
}

// bailianSaid is the status with the vendor's own code and reason, as
// the token endpoint tells them, without what the endpoint must not echo
// into a card: the AccessKey ID and Secret themselves.
func bailianSaid(status, code, message string, secret ...string) string {
	said := strings.TrimSpace(code + " " + strings.TrimSpace(message))
	if said == "" {
		return status
	}
	return status + " " + bailianClean(said, secret...)
}

// bailianRefusesKey says the token endpoint's code is about the access
// key or its rights (InvalidAccessKeyId.NotFound, SignatureDoesNotMatch,
// UnauthorizedOperation…), not the plan.
func bailianRefusesKey(code string, status int) bool {
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		return true
	}
	c := strings.ToLower(code)
	for _, w := range []string{"accesskey", "signature", "unauthorized", "forbidden", "no_permission", "authfailure"} {
		if strings.Contains(c, w) {
			return true
		}
	}
	return false
}

// bailianConsoleParams is the form's params field: the console API
// asked through the gateway, as the CLI builds it
// (packages/core/src/console/gateway.ts buildGatewayParams).
type bailianConsoleParams struct {
	Api  string `json:"Api"`
	V    string `json:"V"`
	Data struct {
		CornerstoneParam struct {
			Protocol       string `json:"protocol"`
			Console        string `json:"console"`
			ProductCode    string `json:"productCode"`
			SwitchUserType int    `json:"switchUserType"`
			ConsoleSite    string `json:"consoleSite"`
		} `json:"cornerstoneParam"`
	} `json:"Data"`
}

// bailianConsoleParamsOf is the gateway's envelope for one console API.
func bailianConsoleParamsOf(api string) bailianConsoleParams {
	var p bailianConsoleParams
	p.Api, p.V = api, "1.0"
	c := &p.Data.CornerstoneParam
	c.Protocol, c.Console, c.ProductCode = "V2", "ONE_CONSOLE", "p_efm"
	c.SwitchUserType, c.ConsoleSite = 3, "BAILIAN_ALIYUN"
	return p
}

// bailianLayer is one level of the gateway's answer: what that level
// says of the call it wraps. A level tells success its own way — a
// success flag, a code, an errorCode — and a level that says the call
// failed makes the answer a failure, whatever the levels under it hold.
type bailianLayer struct {
	Success   json.RawMessage `json:"success"`
	Code      json.RawMessage `json:"code"`
	ErrorCode json.RawMessage `json:"errorCode"`
}

// bailianFlag is what a level's success field says: nothing, true, false,
// or present in a shape magpie can't read. A level that gives no flag is
// left to its code; one that gives a flag magpie can't read is not taken
// for a success either way — a reply magpie can't confirm is not
// published, and 0 or another shape is not guessed at.
type bailianFlag int

const (
	bailianFlagAbsent bailianFlag = iota
	bailianFlagTrue
	bailianFlagFalse
	bailianFlagUnreadable
)

// flag is the level's success field: a boolean or the word true/false,
// as the gateway tells them; any other value that is there is unreadable.
// JSON null is a value too, and isn't a false.
func (l bailianLayer) flag() bailianFlag {
	raw := strings.TrimSpace(string(l.Success))
	if raw == "" {
		return bailianFlagAbsent
	}
	var b bool
	if raw != "null" && json.Unmarshal(l.Success, &b) == nil {
		if b {
			return bailianFlagTrue
		}
		return bailianFlagFalse
	}
	var said string
	if json.Unmarshal(l.Success, &said) == nil {
		switch {
		case strings.EqualFold(strings.TrimSpace(said), "true"):
			return bailianFlagTrue
		case strings.EqualFold(strings.TrimSpace(said), "false"):
			return bailianFlagFalse
		}
	}
	return bailianFlagUnreadable
}

// failure is what the level says went wrong, false when it says nothing
// did: a success flag that is there and can't be read is a failure; so is
// success false, with the errorCode it gives or a failure without one; an
// errorCode alone is one too; a code is one unless it is a code of a
// successful answer (bailianSuccessCode).
func (l bailianLayer) failure() (string, bool) {
	code := bailianFieldText(l.Code)
	switch l.flag() {
	case bailianFlagUnreadable:
		return "its success flag is unreadable", true
	case bailianFlagFalse:
		if said := bailianFieldText(l.ErrorCode); said != "" {
			return said, true
		}
		return bailianOrUnknown(code), true
	}
	if said := bailianFieldText(l.ErrorCode); said != "" {
		return said, true
	}
	if code != "" && !bailianSuccessCode(code) {
		return code, true
	}
	return "", false
}

// bailianSuccessCode says code is one a successful answer carries, as
// the gateway tells them: its root's 200 and the business level's
// SUCCESS, either as a number or as a string. A code of its own is not
// taken for a success.
func bailianSuccessCode(code string) bool {
	switch strings.ToUpper(strings.TrimSpace(code)) {
	case "200", "SUCCESS":
		return true
	}
	return false
}

// bailianCredentials matches the shapes a console token and an AccessKey
// ID are written in, for an error that must be safe to show even where
// the value isn't at hand.
var bailianCredentials = regexp.MustCompile(`\b(ct-|LTAI)[A-Za-z0-9._~+/=-]{4,}`)

// bailianClean is s for an error a card shows: the values given, a
// console token's or an AccessKey ID's shape, and anything past a
// sensible length are taken out. A vendor's diagnostic is its own, but
// magpie's errors never carry a Secret or a token back.
func bailianClean(s string, secrets ...string) string {
	for _, secret := range secrets {
		if len(secret) >= 6 {
			s = strings.ReplaceAll(s, secret, "…")
		}
	}
	s = bailianCredentials.ReplaceAllString(s, "$1…")
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}

// bailianConsoleCall asks one console API with the token and gives its
// business payload, unwrapped of the gateway's envelopes. The gateway
// answers its own failures with HTTP 200, so each level of the answer is
// asked what it says: the root, data, DataV2 and the business level.
// NotLogined is the token aged out, told apart for the one retry. secret
// is what an answer must not echo back into an error.
func bailianConsoleCall(ctx context.Context, token, api string, secret ...string) (json.RawMessage, error) {
	params, err := json.Marshal(bailianConsoleParamsOf(api))
	if err != nil {
		return nil, err
	}
	form := url.Values{"params": {string(params)}, "region": {bailianConsoleRegion}}.Encode()
	u := "https://" + bailianConsoleHost + bailianConsolePath +
		"?action=" + bailianConsoleAction + "&product=" + bailianConsoleProduct + "&api=" + url.QueryEscape(api)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, strings.NewReader(form))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Authorization", "Bearer "+token)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("Bailian: %w", err)
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("Bailian: %w", err)
	}
	clean := func(s string) string { return bailianClean(s, append([]string{token}, secret...)...) }
	if res.StatusCode >= 300 {
		// the body is cleaned whole, then cut short: truncating first would
		// leave a long token's prefix unable to match the value it is
		return nil, fmt.Errorf("Bailian: the console gateway answered %s: %s", res.Status, clean(string(raw)))
	}
	// the gateway's own level, then the console API's, then the business
	// data: data.DataV2.data.data. An outer HTTP 200 says nothing of the
	// inner calls; each level says its own.
	var env struct {
		bailianLayer
		Data *struct {
			bailianLayer
			DataV2 *struct {
				bailianLayer
				Data *struct {
					bailianLayer
					Data json.RawMessage `json:"data"`
				} `json:"data"`
			} `json:"DataV2"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("Bailian: a reply magpie can't read: %s", shapeOf(raw))
	}
	said, failed := env.failure()
	if !failed && env.Data == nil {
		return nil, fmt.Errorf("Bailian: a reply magpie can't read: %s", shapeOf(raw))
	}
	if !failed {
		said, failed = env.Data.failure()
	}
	if !failed && env.Data.DataV2 == nil {
		return nil, fmt.Errorf("Bailian: a reply magpie can't read: %s", shapeOf(raw))
	}
	if !failed {
		said, failed = env.Data.DataV2.failure()
	}
	if !failed && env.Data.DataV2.Data == nil {
		return nil, fmt.Errorf("Bailian: a reply magpie can't read: %s", shapeOf(raw))
	}
	if !failed {
		said, failed = env.Data.DataV2.Data.failure()
	}
	if failed {
		said = clean(said)
		if strings.Contains(said, "NotLogined") {
			return nil, errBailianNotLogined
		}
		return nil, fmt.Errorf("Bailian: the console gateway said %s", said)
	}
	inner := env.Data.DataV2.Data
	if len(inner.Data) == 0 {
		return nil, fmt.Errorf("Bailian: the console gateway told no %s data: %s", apiSuffix(api), shapeOf(raw))
	}
	return inner.Data, nil
}

// apiSuffix is the console API's own name, for a reply that reaches it.
func apiSuffix(api string) string {
	if _, after, ok := strings.Cut(api, "apikeyMgr."); ok {
		return after
	}
	return api
}

func bailianOrUnknown(code string) string {
	if code = strings.TrimSpace(code); code == "" {
		return "it failed (no error code)"
	}
	return code
}

// bailianFieldText is a layer's code or errorCode as the gateway tells
// it, whatever JSON kind it came as: a string, a number, or the JSON
// text of whatever else.
func bailianFieldText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return strings.TrimSpace(s)
	}
	if len(raw) > 0 {
		return string(raw)
	}
	return ""
}

// bailianConsole is one console API read with the access key: the kept
// token, and when the gateway calls it NotLogined, one fresh one and
// one retry, never more.
func bailianConsole(ctx context.Context, ak, sk, api string) (json.RawMessage, error) {
	token, err := bailianToken(ctx, ak, sk, false)
	if err != nil {
		return nil, err
	}
	payload, err := bailianConsoleCall(ctx, token, api, ak, sk)
	if err == nil || !errors.Is(err, errBailianNotLogined) {
		return payload, err
	}
	bailianForgetToken(ak, sk, token)
	if token, err = bailianToken(ctx, ak, sk, true); err != nil {
		return nil, err
	}
	return bailianConsoleCall(ctx, token, api, ak, sk)
}

// bailianSubscription is the personal plan the subscription API tells:
// the spec the account is on, whether it is valid, when it runs and
// whether it renews. Times are epoch milliseconds.
type bailianSubscription struct {
	SpecCode      string `json:"specCode"`
	Status        string `json:"status"`
	StartTime     int64  `json:"startTime"`
	EndTime       int64  `json:"endTime"`
	AutoRenewFlag *bool  `json:"autoRenewFlag"`
}

// empty says the reply told no plan at all.
func (s bailianSubscription) empty() bool {
	return s.SpecCode == "" && s.Status == "" && s.StartTime == 0 && s.EndTime == 0
}

// bailianUsage is the month's use the usage API tells: the share used,
// in [0,1], and when the month resets (epoch milliseconds).
type bailianUsage struct {
	Per1MonthPercentage *float64 `json:"per1MonthPercentage"`
	Per1MonthResetTime  int64    `json:"per1MonthResetTime"`
}

// bailianQuotaConfig is each spec's own allowances a month: the plan's
// monthly credits, and its five_hour rate limit, which is no allowance
// the account holds and makes no window.
type bailianQuotaConfig map[string]struct {
	FiveHour float64 `json:"five_hour"`
	Monthly  float64 `json:"monthly"`
}

// monthlyOf is the monthly credits of the spec the subscription named:
// its own entry and no other's. A spec the config doesn't list, or a
// subscription that named none, makes no count up — another spec's
// credits, or the config's only entry, would be a total magpie can't
// back.
func (c bailianQuotaConfig) monthlyOf(spec string) (float64, bool) {
	if c == nil || spec == "" {
		return 0, false
	}
	q, ok := c[spec]
	if !ok || q.Monthly <= 0 {
		return 0, false
	}
	return q.Monthly, true
}

// bailianPlanQuota is the account's personal plan as a card, read with
// its AccessKey ID and Secret. The card shows the month's share used
// and, when the spec's quota config is told, the credits it counts; the
// plan's end and whether it renews; nothing a read left unknown is made
// 0 or a promise of fresh credits.
func bailianPlanQuota(ctx context.Context, p Provider) SubscriptionQuota {
	q := SubscriptionQuota{Provider: p.ID, Name: p.Name, Icon: p.Icon, Plan: bailianPlanName, Windows: []QuotaWindow{}}
	subRaw, err := bailianConsole(ctx, p.AccessKeyID, p.SecretAccessKey, bailianSubscriptionAPI)
	if err != nil {
		q.Error = err.Error()
		return q
	}
	var sub bailianSubscription
	if err := json.Unmarshal(subRaw, &sub); err != nil {
		q.Error = fmt.Sprintf("Bailian told the plan in a form magpie doesn't know: %s", shapeOf(subRaw))
		return q
	}
	if sub.empty() {
		q.Error = "this Alibaba Cloud account has no Bailian Token Plan personal that its access key can see"
		return q
	}
	if sub.SpecCode != "" {
		q.Plan += " · " + sub.SpecCode
	}
	if sub.Status != "" && !strings.EqualFold(sub.Status, "VALID") {
		q.Plan += " · " + sub.Status
	}
	usageRaw, err := bailianConsole(ctx, p.AccessKeyID, p.SecretAccessKey, bailianUsageAPI)
	if err != nil {
		q.Error = err.Error()
		return q
	}
	var usage bailianUsage
	if err := json.Unmarshal(usageRaw, &usage); err != nil || usage.Per1MonthPercentage == nil {
		q.Error = fmt.Sprintf("Bailian told no share of the month the plan has used: %s", shapeOf(usageRaw))
		return q
	}
	ratio := *usage.Per1MonthPercentage
	w := QuotaWindow{Name: "Month", Span: 30 * 24 * time.Hour, Used: max(0, min(100, ratio*100)), Unit: "credits"}
	if usage.Per1MonthResetTime > 0 {
		t := time.UnixMilli(usage.Per1MonthResetTime)
		w.ResetsAt = &t
	}
	// the credits the share is a share of: the spec's own month, without
	// the add-on packages and harness benefits that are other purchases.
	// A read that failed is an error, not a younger reading without
	// credits: the last complete one is kept as any plan's is.
	cfgRaw, cfgErr := bailianConsole(ctx, p.AccessKeyID, p.SecretAccessKey, bailianQuotaConfigAPI)
	if cfgErr != nil {
		q.Error = "Bailian told the plan's month but not its credits: " + strings.TrimPrefix(cfgErr.Error(), "Bailian: ")
		return q
	}
	var cfg bailianQuotaConfig
	if err := json.Unmarshal(cfgRaw, &cfg); err != nil {
		q.Error = fmt.Sprintf("Bailian told the plan's credits in a form magpie doesn't know: %s", shapeOf(cfgRaw))
		return q
	}
	if monthly, ok := cfg.monthlyOf(sub.SpecCode); ok {
		w.Limit = monthly
		w.Amount = monthly * w.Used / 100
	}
	q.Windows = []QuotaWindow{w}
	// the plan's own term: when it ends and whether it renews then. A
	// plan not on auto-renew is said to expire, never to refill.
	if sub.EndTime > 0 {
		t := time.UnixMilli(sub.EndTime)
		q.Until = &t
	}
	if sub.AutoRenewFlag != nil {
		q.Renew = map[bool]string{true: "auto", false: "off"}[*sub.AutoRenewFlag]
	}
	return q
}

// bailianPlanQuotas is a card for each Bailian Token Plan 个人版 a
// provider in use has the account's AccessKey ID and Secret for: one
// for each access key, named for the first such provider. The plan is
// the account's, so several sk- keys of one account read one card, and
// a plan no provider on it has the access key for is a card that says
// so (accessKeyCard, #1821).
func bailianPlanQuotas(ctx context.Context) []SubscriptionQuota {
	seen := map[string]bool{}
	var jobs []Provider
	keyed := false
	var keyless []Provider
	for _, p := range planViews(All()) {
		if p.Hidden || p.Off || !TakesBailianAccessKey(p) {
			continue
		}
		if p.AccessKeyID == "" || p.SecretAccessKey == "" {
			keyless = append(keyless, p)
			continue
		}
		keyed = true
		if seen[p.AccessKeyID] || !wantsCard(ctx, p.ID, "") {
			continue
		}
		seen[p.AccessKeyID] = true
		jobs = append(jobs, p)
	}
	out := make([]SubscriptionQuota, len(jobs))
	var wg sync.WaitGroup
	for i, p := range jobs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out[i] = keepReading(ctx, bailianPlanQuota(p.Via(ctx), p), keyTag("bailian personal", p.AccessKeyID))
		}()
	}
	wg.Wait()
	for _, p := range keyless {
		if keyed || seen["none"] || !wantsCard(ctx, p.ID, "") {
			continue
		}
		seen["none"] = true
		out = append(out, accessKeyCard(p, bailianPlanName))
	}
	return out
}
