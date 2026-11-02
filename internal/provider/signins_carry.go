package provider

// Sign-ins a backup or a sync carries, when the user turned that on
// ("Include sign-ins", off by default): the subscription accounts magpie
// keeps in logins.json (Codex, Claude Code, Gemini CLI, Copilot, …) and
// the plugins' sign-ins in plugin-auth.json. They go only inside a sealed
// backup, and are put back without writing over what is newer here:
//
//   - an account is told by who it is (email and organization or
//     workspace, or its name) and matched against every account here
//     that could be the same one (sameLogin, and for Codex the refresh
//     token too). One that matches more than one is left as it is here;
//   - of two copies of one account, the one renewed later stays (the
//     vendor rotates refresh tokens, so the older one may be spent); when
//     either can't say when it was renewed, the copy here stays;
//   - the agent's own sign-in here (Claude Code's keychain item, Codex's
//     auth.json and the accounts those are signed in to) is never written
//     over. Codex's auth.json is written, temp-and-rename, only when Codex
//     is signed out here and the user hasn't removed its account; Claude
//     Code's keychain is never touched: a saved Claude account is served
//     by the gateway as it is;
//   - a plugin whose auth hook says rotating (the vendor spends a refresh
//     token once: renewed, the old one answers invalid_grant) is never
//     carried at all — a copy on a second computer would race this one to
//     renew it, and the vendor would turn the loser away for good, with
//     no newer generation anywhere to come back from (#5583). It stays
//     signed in on the computer that signed in, and one carried by a
//     backup or a sync made before the plugin said so is left out here.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/lastgood"
	"github.com/yetone/magpie/internal/plugin"
)

// SignIns are the sign-ins a backup carries.
type SignIns struct {
	// Logins are the subscription accounts magpie keeps, with their
	// credentials: logins.json's records as written there.
	Logins []savedLogin `json:"logins,omitempty"`
	// Plugins are the plugins' sign-ins by key, plugin-auth.json's.
	Plugins map[string]json.RawMessage `json:"plugins,omitempty"`
	// PluginLogins binds each carried auth slot to its saved routing owner.
	// Labels are display metadata; Agent, Home and ID supply the identity.
	PluginLogins map[string]savedLogin `json:"pluginLogins,omitempty"`
	// Codex is the record (SignInID) of the account Codex was signed in to
	// where these were collected: put back where Codex is signed out.
	Codex string `json:"codex,omitempty"`
}

// SignInsResult says what putting sign-ins back did.
type SignInsResult struct {
	Added   int `json:"added,omitempty"`   // accounts that weren't here
	Updated int `json:"updated,omitempty"` // accounts here renewed later where they came from
	// Kept is the accounts left as they are here: renewed later here, the
	// agent's own sign-in, or more than one account here could be it.
	Kept int `json:"kept,omitempty"`
	// Codex: Codex was signed out here, and is signed in now.
	Codex bool `json:"codex,omitempty"`
}

// signInRec is one carried sign-in: an account or a plugin's.
type signInRec struct {
	id    string
	hash  string    // of the credential alone
	at    time.Time // when it was renewed, zero when it doesn't say
	login *savedLogin
	key   string // plugin-auth.json's key
	raw   json.RawMessage
	owner *savedLogin
}

// stateHash checks the local credential and routing owner together before
// apply. It is not the portable credential hash used by sync deletion rules.
func (r signInRec) stateHash() string {
	if r.owner == nil {
		return r.hash
	}
	b, _ := json.Marshal([]string{r.hash, r.owner.Agent, r.owner.Home, r.owner.ID})
	return credHash(b)
}

// carried is whether an account of logins.json goes with the sign-ins:
// one holding its credential in the file, magpie's own copy — not the
// agent's own sign-in read in place, one kept in a folder of its own
// (Home), one removed in magpie, a plugin's or a retired built-in's.
func carried(l savedLogin) bool {
	return !l.own() && l.Home == "" && l.Hidden == "" &&
		!strings.HasPrefix(l.Agent, "plugin:") && !BuiltinRetired(l.Agent)
}

// loginRecID is who an account is, the same on every computer.
func loginRecID(l savedLogin) string {
	var who, org string
	switch l.Agent {
	case "claude":
		who, org = claudeWho(l.Profile)
	case "codex":
		who, org = codexWho(l.Auth)
	}
	if who != "" && org != "" {
		return l.Agent + "|" + strings.ToLower(who) + "|" + org
	}
	return l.Agent + "|" + accountKey(l.User)
}

func pluginRecID(key string, raw json.RawMessage) string {
	if who := plugin.AuthWho(raw); who != "" {
		return "plugin|" + plugin.ProviderOf(key) + "|" + strings.ToLower(who)
	}
	return "plugin|" + key
}

func credHash(b []byte) string {
	var c bytes.Buffer
	if json.Compact(&c, b) != nil {
		c.Reset()
		c.Write(b)
	}
	s := sha256.Sum256(c.Bytes())
	return hex.EncodeToString(s[:12])
}

// credTime is when a credential was renewed, as it says itself: Codex's
// last_refresh, an access token's expiry (Claude's expiresAt, Google's
// expiry_date, a plugin's expires), else when magpie renewed it. Zero:
// it doesn't say.
func credTime(auth json.RawMessage, renewed time.Time) time.Time {
	var a struct {
		LastRefresh string `json:"last_refresh"`
		Claude      *struct {
			ExpiresAt int64 `json:"expiresAt"`
		} `json:"claudeAiOauth"`
		ExpiresAt  int64 `json:"expiresAt"`
		ExpiryDate int64 `json:"expiry_date"`
		Expires    int64 `json:"expires"`
	}
	if json.Unmarshal(auth, &a) == nil {
		if t, err := time.Parse(time.RFC3339Nano, a.LastRefresh); err == nil {
			return t
		}
		for _, ms := range []int64{func() int64 {
			if a.Claude != nil {
				return a.Claude.ExpiresAt
			}
			return 0
		}(), a.ExpiresAt, a.ExpiryDate, a.Expires} {
			if ms > 0 {
				return time.UnixMilli(ms)
			}
		}
	}
	return renewed
}

// records are s's sign-ins by id. Two of one id (accounts told apart here
// in a way that doesn't carry) are left out both: neither is put over the
// other, nor anything here. A plugin whose provider says its refresh
// token rotates (Rotating, the auth hook's rotating) is left out too:
// its sign-in is one computer's alone (#5583).
func (s *SignIns) records() map[string]signInRec {
	out := map[string]signInRec{}
	if s == nil {
		return out
	}
	twice := map[string]bool{}
	add := func(r signInRec) {
		if _, ok := out[r.id]; ok || twice[r.id] {
			delete(out, r.id)
			twice[r.id] = true
			return
		}
		out[r.id] = r
	}
	for i := range s.Logins {
		l := s.Logins[i]
		if !carried(l) {
			continue
		}
		add(signInRec{id: loginRecID(l), hash: credHash(l.Auth), at: credTime(l.Auth, l.Renewed), login: &l})
	}
	if len(s.Plugins) > 0 {
		rotating := rotatingPlugins()
		for k, raw := range s.Plugins {
			if rotating[plugin.ProviderOf(k)] {
				continue // a refresh token the vendor spends once (#5583)
			}
			var at time.Time
			if ms := plugin.AuthExpires(raw); ms > 0 {
				at = time.UnixMilli(ms)
			}
			r := signInRec{id: pluginRecID(k, raw), hash: credHash(raw), at: at, key: k, raw: raw}
			if l, ok := s.PluginLogins[k]; ok {
				r.owner = &l
			}
			add(r)
		}
	}
	return out
}

// rotatesKnown are the community plugins whose vendor spends a refresh
// token once, read from their sources: MiniMax Code ("MiniMax spends a
// refresh token once", invalid_grant 400 after) and Qoder ("a refresh
// token is spent once, so two refreshes would spend it twice"), and the
// six whose auth hooks say rotating since plugins #1027: Cline (a spent
// token is 400 invalid_grant), CodeArts and OfficeAce ("one-time refresh
// token"), Factory (WorkOS rotates), Nous Portal (refresh_token_reused),
// SenseNova (Hydra rotates) and WorkBuddy. The hooks say so too; this list
// holds for a sync that runs before the plugin host's list is read.
var rotatesKnown = []string{
	"cline", "codearts", "factory", "minimax-code", "minimax-code-global", "nous-portal",
	"officeace", "qoder", "qoder-cn", "sensenova-token-plan", "workbuddy", "workbuddy-ai",
}

// rotatingPlugins are the ids of the plugins whose auth hook says their
// refresh tokens rotate (Rotating): a sign-in of theirs is never carried,
// so no computer holds a copy of a token the vendor lets be spent once
// (#5583). Besides what a plugin says of its own, the community plugins
// known to rotate (rotatesKnown) are never carried either: their auth
// hooks said nothing of it when this landed, and the plugin host's list
// may not be read yet when a sync runs at start.
func rotatingPlugins() map[string]bool {
	out := map[string]bool{}
	for _, id := range rotatesKnown {
		out[id] = true
	}
	for _, p := range plugin.Cached() {
		if p.Rotating {
			out[p.ID] = true
		}
	}
	return out
}

// Hashes are s's sign-ins as their ids and credential hashes. Local saved
// owners may differ after a successful sync and are not credential edits.
func (s *SignIns) Hashes() map[string]string {
	out := map[string]string{}
	for id, r := range s.records() {
		out[id] = r.hash
	}
	return out
}

// readLoginsFile is logins.json's accounts, read now: an error when it
// can't be read, never none.
func readLoginsFile() ([]savedLogin, error) {
	b, err := lastgood.Read(loginsPath(), validLogins)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("logins.json can't be read: %w", err)
	}
	ls, err := parseLogins(b)
	if err != nil {
		return nil, fmt.Errorf("logins.json can't be read: %w", err)
	}
	return ls, nil
}

// CollectSignIns gathers the sign-ins to carry. Codex's own sign-in here
// is taken from its auth.json, fresher than magpie's copy of it. A file
// that can't be read is an error: carried as no sign-ins, a sync would
// take them away elsewhere.
func CollectSignIns() (*SignIns, error) {
	if discoveryOff() {
		return nil, errAccountDiscoveryOff
	}
	loginsMu.Lock()
	ls, err := readLoginsFile()
	if err != nil {
		loginsMu.Unlock()
		return nil, err
	}
	pl, err := plugin.AuthEntries()
	loginsMu.Unlock()
	if err != nil {
		return nil, fmt.Errorf("the plugins' sign-ins can't be read: %w", err)
	}
	s := &SignIns{Plugins: pl}
	// a plugin whose refresh token rotates is left out: its sign-in is one
	// computer's alone, never a backup's or a sync's to copy (#5583)
	if len(s.Plugins) > 0 {
		rotating := rotatingPlugins()
		for k := range s.Plugins {
			if rotating[plugin.ProviderOf(k)] {
				delete(s.Plugins, k)
			}
		}
		if len(s.Plugins) == 0 {
			s.Plugins = nil
		}
	}
	for _, l := range ls {
		if strings.HasPrefix(l.Agent, "plugin:") {
			if _, ok := pl[l.Home]; ok && l.Agent == "plugin:"+plugin.ProviderOf(l.Home) {
				if s.PluginLogins == nil {
					s.PluginLogins = map[string]savedLogin{}
				}
				s.PluginLogins[l.Home] = savedLogin{ID: loginID(l), Agent: l.Agent, Home: l.Home, User: l.User, On: l.On, First: l.First}
			}
		}
	}
	live, _, lerr := readCodexLogin(codexHome(), false)
	for _, l := range ls {
		if !carried(l) {
			continue
		}
		l.Held, l.was = false, ""
		if l.Agent == "codex" && lerr == nil && liveCodex(l, live) {
			l.Auth = append(json.RawMessage(nil), bytes.TrimSpace(live)...)
			s.Codex = loginRecID(l)
		}
		s.Logins = append(s.Logins, l)
	}
	if len(s.Plugins) == 0 {
		s.Plugins = nil
	}
	return s, nil
}

// liveCodex says l is the account Codex is signed in to with auth.
func liveCodex(l savedLogin, auth []byte) bool {
	if l.Agent != "codex" || len(auth) == 0 {
		return false
	}
	in := savedLogin{Agent: "codex", Auth: auth}
	e, w := codexWho(auth)
	in.User = e
	if w == "" {
		in.User = "\x00" // a name alone doesn't match: unknown
	}
	return sameLogin(l, in) || sameRefresh(l.Auth, auth) && !codexWorkspacesApart(l.Auth, auth)
}

// SignInPlan is what a sync puts in and takes away here (MergeSignIns),
// made over the sign-ins it read here: a sign-in changed here since is
// left alone.
type SignInPlan struct {
	put    []signInRec
	remove []string
	seen   map[string]string // id → local stateHash for apply CAS; absent: not here
	err    error
	codex  *signInRec // for Codex where it is signed out
}

// Empty says the plan changes nothing.
func (p SignInPlan) Empty() bool {
	return p.err == nil && len(p.put) == 0 && len(p.remove) == 0 && p.codex == nil
}

// newer says a was renewed after b, both saying when.
func newer(a, b signInRec) bool { return !a.at.IsZero() && !b.at.IsZero() && a.at.After(b.at) }

// MergeSignIns merges this computer's sign-ins with the server's. prev is
// the sign-ins (id → hash) the last sync left on the server and here, nil
// for none known. Of an account on both sides, the one renewed later goes
// on (when neither says, the side that changed since prev); one gone from
// a side since prev was removed there, and goes from the other, unless it
// changed there since. It gives what goes up, what to do here, and what
// goes up's hashes.
func MergeSignIns(local, remote *SignIns, prev map[string]string) (*SignIns, SignInPlan, map[string]string) {
	lr, rr := local.records(), remote.records()
	plan := SignInPlan{seen: map[string]string{}}
	for id, r := range lr {
		plan.seen[id] = r.stateHash()
	}
	for id, l := range lr {
		r, ok := rr[id]
		if !ok || l.login != nil || r.login != nil {
			continue
		}
		if l.key != r.key || l.owner != nil && r.owner != nil && (l.owner.ID != r.owner.ID || l.owner.Agent != r.owner.Agent || l.owner.Home != r.owner.Home) {
			reserved, err := carriedPluginReservation(l, r)
			if err != nil {
				plan.err = err
			} else if reserved {
				plan.err = fmt.Errorf("plugin sign-in routing owner conflict")
			}
		}
		// An older peer cannot erase a known owner, even with a newer
		// credential. A different slot must be checked by applyPlugins.
		if l.key == r.key {
			if r.owner == nil {
				r.owner = l.owner
				rr[id] = r
			}
			if l.owner == nil {
				l.owner = r.owner
				lr[id] = l
			}
		}
	}
	shared := map[string]signInRec{}
	for id, l := range lr {
		r, ok := rr[id]
		p, was := prev[id]
		switch {
		case ok && r.hash == l.hash:
			shared[id] = l
		case ok && newer(l, r):
			shared[id] = l
		case ok && newer(r, l):
			shared[id] = r
			plan.put = append(plan.put, r)
		case ok:
			// neither says it was renewed later: the side changed since
			// the last sync goes on, and when both did, or there was no
			// last sync, the one here stays here
			if lc, rc := !was || p != l.hash, !was || p != r.hash; rc && !lc {
				shared[id] = r
				plan.put = append(plan.put, r)
			} else {
				shared[id] = l
			}
		case remote != nil && was && p == l.hash:
			// removed on another computer, unchanged here since
			plan.remove = append(plan.remove, id)
		default:
			shared[id] = l
		}
	}
	for id, r := range rr {
		if _, ok := lr[id]; ok {
			continue
		}
		if p, was := prev[id]; was && p == r.hash {
			continue // removed here since the last sync
		}
		shared[id] = r
		plan.put = append(plan.put, r)
	}
	planned := map[string]bool{}
	for _, r := range plan.put {
		planned[r.id] = true
	}
	for id, r := range shared {
		if before, ok := plan.seen[id]; ok && r.stateHash() != before && !planned[id] {
			plan.put = append(plan.put, r)
		}
	}
	out := &SignIns{}
	hashes := map[string]string{}
	for _, id := range slices.Sorted(maps.Keys(shared)) {
		r := shared[id]
		hashes[id] = r.hash
		if r.login != nil {
			out.Logins = append(out.Logins, *r.login)
			continue
		}
		if out.Plugins == nil {
			out.Plugins = map[string]json.RawMessage{}
		}
		out.Plugins[r.key] = r.raw
		if r.owner != nil {
			if out.PluginLogins == nil {
				out.PluginLogins = map[string]savedLogin{}
			}
			out.PluginLogins[r.key] = *r.owner
		}
	}
	switch {
	case local != nil && local.Codex != "":
		out.Codex = local.Codex
	case remote != nil:
		out.Codex = remote.Codex
	}
	// a computer joining: Codex, signed out here, is signed in to the
	// account it was on elsewhere. Only then: one signed out of it here
	// later stays signed out
	if c, ok := shared[out.Codex]; ok && c.login != nil && prev == nil {
		plan.codex = &c
	}
	sort.Slice(plan.put, func(i, j int) bool { return plan.put[i].id < plan.put[j].id })
	return out, plan, hashes
}

// RestoreSignIns puts a backup's sign-ins in: those not here are added,
// those here renewed later where the backup was made are updated, nothing
// is taken away, and Codex is signed in to its account when it is signed
// out here. Selected providers are checked before any writes and supply
// the incoming owner evidence when their registry has not been applied yet.
func RestoreSignIns(s *SignIns, providers ...Provider) (SignInsResult, error) {
	if s == nil {
		return SignInsResult{}, nil
	}
	if err := CheckSignInsRouting(s, providers); err != nil {
		return SignInsResult{}, err
	}
	here, err := CollectSignIns()
	if err != nil {
		return SignInsResult{}, err
	}
	lr := here.records()
	plan := SignInPlan{seen: map[string]string{}}
	for id, r := range lr {
		plan.seen[id] = r.stateHash()
	}
	var kept int
	for _, r := range s.records() {
		l, ok := lr[r.id]
		switch {
		case !ok, newer(r, l), r.hash == l.hash && r.owner != nil && r.stateHash() != l.stateHash():
			plan.put = append(plan.put, r)
		case l.hash != r.hash:
			kept++
		}
	}
	if c, ok := s.records()[s.Codex]; ok && c.login != nil {
		plan.codex = &c
	}
	res, err := ApplySignIns(plan, providers...)
	res.Kept += kept
	return res, err
}

// ApplySignIns does a plan here: under logins.json's lock, and
// plugin-auth.json's, each read afresh. Providers are only those selected by
// the caller and checked before it applies their registry.
func ApplySignIns(p SignInPlan, providers ...Provider) (SignInsResult, error) {
	var res SignInsResult
	if p.err != nil {
		return res, p.err
	}
	if p.Empty() {
		return res, nil
	}
	if discoveryOff() {
		return res, errAccountDiscoveryOff
	}
	gone := map[string]bool{}
	for _, id := range p.remove {
		gone[id] = true
	}
	var logins, plugins []signInRec
	for _, r := range p.put {
		if r.login != nil {
			logins = append(logins, r)
		} else {
			plugins = append(plugins, r)
		}
	}
	if err := applyLogins(logins, gone, p.seen, &res, providers); err != nil {
		return res, err
	}
	if err := applyPlugins(plugins, gone, p.seen, &res, providers); err != nil {
		return res, err
	}
	if p.codex != nil {
		ok, err := signCodexIn(*p.codex.login)
		if err != nil {
			return res, err
		}
		res.Codex = ok
	}
	return res, nil
}

func applyLogins(put []signInRec, gone map[string]bool, seen map[string]string, res *SignInsResult, providers []Provider) error {
	if len(put) == 0 && !hasPrefixed(gone, "plugin|", false) {
		return nil
	}
	loginsMu.Lock()
	defer loginsMu.Unlock()
	ls, err := readLoginsFile()
	if err != nil {
		return err
	}
	live, _, lerr := readCodexLogin(codexHome(), false)
	// the agent's own sign-in, and the accounts it is signed in to: never
	// written over nor taken away from here
	protected := func(l savedLogin) bool {
		return l.Held || (lerr == nil && liveCodex(l, live))
	}
	now := map[string]string{} // as it is here now
	for _, l := range ls {
		if carried(l) {
			now[loginRecID(l)] = credHash(l.Auth)
		}
	}
	changed := false
	for _, r := range put {
		in := *r.login
		if now[r.id] != seen[r.id] { // changed here since it was read
			res.Kept++
			continue
		}
		if in.Agent == "codex" {
			if err := checkCarriedCodexOwner(in, ls, providers); err != nil {
				return err
			}
		}
		var at []int
		for i, l := range ls {
			if l.Agent == in.Agent && (sameLogin(l, in) || in.Agent == "codex" && sameRefresh(l.Auth, in.Auth) && !codexWorkspacesApart(l.Auth, in.Auth) || loginRecID(l) == r.id) {
				at = append(at, i)
			}
		}
		switch {
		case len(at) == 0:
			in.Held, in.First, in.Order, in.was = false, false, 0, ""
			if slices.ContainsFunc(ls, func(l savedLogin) bool { return l.ID != "" && l.ID == in.ID }) {
				in.ID = ""
			}
			if in.Agent == "codex" {
				if err := checkCarriedCodexOwner(in, ls, providers); err != nil {
					return err
				}
			}
			if in.Agent == "codex" && accountRemoved("codex") || in.Agent == "claude" && accountRemoved("claude") {
				res.Kept++
				continue
			}
			ls = append(ls, in)
			res.Added++
			changed = true
		case len(at) > 1:
			res.Kept++ // which of them it is isn't known
		default:
			l := &ls[at[0]]
			if !carried(*l) || protected(*l) || loginRecID(*l) != r.id {
				res.Kept++
				continue
			}
			if credHash(l.Auth) == r.hash {
				continue
			}
			l.Auth, l.Profile, l.Plan, l.AccessSKU = in.Auth, in.Profile, in.Plan, in.AccessSKU
			l.Renewed, l.Lapsed, l.Refused, l.Seen = in.Renewed, in.Lapsed, in.Refused, in.Seen
			res.Updated++
			changed = true
		}
	}
	for i := 0; i < len(ls); i++ {
		l := ls[i]
		id := loginRecID(l)
		if !carried(l) || !gone[id] || protected(l) || credHash(l.Auth) != seen[id] {
			continue
		}
		ls = slices.Delete(ls, i, i+1)
		i--
		changed = true
	}
	if !changed {
		return nil
	}
	return writeLogins(ls)
}

// hasPrefixed says some key of m has (or, want false, lacks) prefix.
func hasPrefixed(m map[string]bool, prefix string, want bool) bool {
	for k := range m {
		if strings.HasPrefix(k, prefix) == want {
			return true
		}
	}
	return false
}

// accountAliasOwner includes retired reservations: neither may be shared with
// a different real workspace by the sign-in import's baseline ID generation.
func accountAliasOwner(p Provider, id string) bool {
	for _, b := range p.AccountAliases {
		if b.AccountID == id {
			return true
		}
	}
	return false
}

func checkCarriedCodexOwner(in savedLogin, ls []savedLogin, providers []Provider) error {
	registry, err := signInRegistry(providers)
	if err != nil {
		return err
	}
	var p Provider
	for _, q := range registry {
		if q.ID == "codex" {
			p = q
			break
		}
	}
	allowNew := false
	for _, q := range providers {
		if q.ID == "codex" && accountAliasOwner(q, loginID(in)) {
			email, workspace := codexWho(in.Auth)
			allowNew = email != "" && workspace != ""
		}
	}
	return checkCodexRoutingOwner(in, ls, p, allowNew)
}

func checkCodexRoutingOwner(in savedLogin, ls []savedLogin, p Provider, allowNew bool) error {
	reserved := accountAliasOwner(p, loginID(in))
	verified := false
	for _, l := range ls {
		ea, wa := codexWho(l.Auth)
		eb, wb := codexWho(in.Auth)
		same := l.Agent == "codex" && ea != "" && wa != "" && eb != "" && wb != "" && strings.EqualFold(ea, eb) && wa == wb
		if loginID(l) == loginID(in) && reserved {
			if !same {
				return fmt.Errorf("Codex sign-in conflicts with a reserved routing owner")
			}
			verified = true
		}
		if same && loginID(l) != loginID(in) && (reserved || accountAliasOwner(p, loginID(l))) {
			return fmt.Errorf("Codex sign-in has inconsistent reserved routing owners")
		}
	}
	if reserved && !verified && !allowNew {
		return fmt.Errorf("Codex sign-in's reserved routing owner cannot be verified here")
	}
	return nil
}

// signInRegistry combines this computer's owner history with the providers
// selected for this import, without publishing any provider changes.
func signInRegistry(providers []Provider) ([]Provider, error) {
	state := readProviderState()
	if state.err != nil {
		return nil, state.err
	}
	out := slices.Clone(state.file.Providers)
	for _, p := range providers {
		if p.ID == "" || p.ID != Slug(p.ID) || p.ID == "magpie" {
			continue
		}
		i := slices.IndexFunc(out, func(q Provider) bool { return q.ID == p.ID })
		if i >= 0 {
			p = KeepSecrets(p, out[i], p.Key == "" && len(p.Keys) == 0)
			if err := mergeRoutingHistory(&p, out[i]); err != nil {
				return nil, err
			}
			out[i] = p
		} else {
			if err := validateRoutingAliases(p); err != nil {
				return nil, err
			}
			out = append(out, p)
		}
	}
	return out, nil
}

// CheckSignInsRouting validates the selected incoming alias registry against
// real saved identities before a caller writes either providers or sign-ins.
// It does not make unrelated backup parts transactional.
func CheckSignInsRouting(s *SignIns, providers []Provider) error {
	if s == nil || len(providers) == 0 {
		return nil
	}
	registry, err := signInRegistry(providers)
	if err != nil {
		return err
	}
	here, err := CollectSignIns()
	if err != nil {
		return err
	}
	loginsMu.Lock()
	ls, err := readLoginsFile()
	loginsMu.Unlock()
	if err != nil {
		return err
	}
	localRecords := here.records()
	identities := append(slices.Clone(ls), s.Logins...)
	for _, r := range s.records() {
		if r.login != nil {
			if r.login.Agent != "codex" {
				continue
			}
			if err := checkCarriedCodexOwner(*r.login, identities, providers); err != nil {
				return err
			}
			in := *r.login
			if slices.ContainsFunc(ls, func(l savedLogin) bool { return l.ID != "" && l.ID == in.ID && !sameLogin(l, in) }) {
				in.ID = ""
				if err := checkCarriedCodexOwner(in, identities, providers); err != nil {
					return err
				}
			}
			continue
		}
		local, exists := localRecords[r.id]
		owners := []signInRec{r}
		if exists {
			owners = append(owners, local)
		}
		if !pluginReservation(registry, owners...) {
			continue
		}
		if r.owner == nil || r.owner.ID == "" || r.owner.Agent != "plugin:"+plugin.ProviderOf(r.key) || r.owner.Home != r.key {
			return fmt.Errorf("incoming plugin alias lacks a verifiable routing owner")
		}
		if exists && (local.key != r.key || local.owner != nil && local.owner.ID != r.owner.ID) {
			return fmt.Errorf("incoming plugin alias conflicts with this computer's routing owner")
		}
		for _, l := range ls {
			sameSlot := l.Agent == r.owner.Agent && l.Home == r.key
			if sameSlot && loginID(l) != r.owner.ID || !sameSlot && loginID(l) == r.owner.ID {
				return fmt.Errorf("incoming plugin alias conflicts with a saved slot")
			}
		}
	}
	return nil
}

// carriedPluginReservation checks only the source and destination owners.
// Without old-format metadata, any history for that namespace is uncertain.
func carriedPluginReservation(records ...signInRec) (bool, error) {
	state := readProviderState()
	if state.err != nil {
		return false, state.err
	}
	return pluginReservation(state.file.Providers, records...), nil
}

func pluginReservation(providers []Provider, records ...signInRec) bool {
	for _, r := range records {
		id := plugin.ProviderOf(r.key)
		for _, p := range providers {
			if p.ID != id && p.ID != PluginID(id) {
				continue
			}
			if r.owner == nil && len(p.AccountAliases) > 0 || r.owner != nil && accountAliasOwner(p, r.owner.ID) {
				return true
			}
		}
	}
	return false
}

func applyPlugins(put []signInRec, gone map[string]bool, seen map[string]string, res *SignInsResult, providers []Provider) error {
	if len(put) == 0 && !hasPrefixed(gone, "plugin|", true) {
		return nil
	}
	// Publish saved owners before their credentials while holding loginsMu
	// through EditAuth's publish and cache invalidation. Discovery cannot
	// drop a not-yet-published slot or mint a replacement owner in between.
	loginsMu.Lock()
	defer loginsMu.Unlock()
	ls, err := readLoginsFile()
	if err != nil {
		return err
	}
	registry, err := signInRegistry(providers)
	if err != nil {
		return err
	}
	ownerAt := func(key string) *savedLogin {
		for i := range ls {
			if ls[i].Agent == "plugin:"+plugin.ProviderOf(key) && ls[i].Home == key {
				return &ls[i]
			}
		}
		return nil
	}
	return plugin.EditAuth(func(m map[string]json.RawMessage) (bool, error) {
		changed, ownersChanged := false, false
		ids := map[string][]string{} // id → keys here
		for k, raw := range m {
			id := pluginRecID(k, raw)
			ids[id] = append(ids[id], k)
		}
		hashOf := func(id string) string {
			if ks := ids[id]; len(ks) == 1 {
				r := signInRec{hash: credHash(m[ks[0]]), owner: ownerAt(ks[0])}
				return r.stateHash()
			}
			return ""
		}
		for _, r := range put {
			if hashOf(r.id) != seen[r.id] {
				res.Kept++
				continue
			}
			ks := ids[r.id]
			if raw, ok := m[r.key]; ok && !slices.Contains(ks, r.key) && plugin.AuthWho(raw) == "" {
				ks = append(ks, r.key)
			}
			if len(ks) > 1 {
				res.Kept++
				continue
			}
			key := r.key
			if len(ks) == 1 {
				key = ks[0]
			}
			if len(ks) == 0 {
				if _, taken := m[key]; taken {
					key = plugin.ProviderOf(r.key) + "#" + r.hash[:8]
				}
			}
			localOwner := ownerAt(key)
			owners := []signInRec{r}
			if localOwner != nil {
				owners = append(owners, signInRec{key: key, owner: localOwner})
			}
			reserved := pluginReservation(registry, owners...)
			if r.owner != nil {
				o := *r.owner
				if o.ID == "" || o.Agent != "plugin:"+plugin.ProviderOf(r.key) || o.Home != r.key {
					return false, fmt.Errorf("plugin sign-in has invalid routing owner metadata")
				}
				// Two unreserved copies may already have different saved IDs.
				// Keep this machine's slot and owner, as credential merges did.
				if reserved && (key != r.key || localOwner != nil && loginID(*localOwner) != o.ID) {
					return false, fmt.Errorf("plugin sign-in slot or routing owner conflict")
				}
				if localOwner == nil {
					for _, l := range ls {
						if loginID(l) == o.ID {
							return false, fmt.Errorf("plugin sign-in routing owner already belongs to another slot")
						}
					}
					ls = append(ls, savedLogin{ID: o.ID, Agent: o.Agent, Home: key, User: o.User, On: o.On, First: o.First})
					ownersChanged = true
				}
			} else if reserved && (len(ks) == 0 || key != r.key || localOwner == nil) {
				return false, fmt.Errorf("plugin sign-in lacks its reserved routing owner")
			}
			switch len(ks) {
			case 0:
				m[key] = r.raw
				res.Added++
				changed = true
			case 1:
				if credHash(m[key]) != r.hash {
					m[key] = r.raw
					res.Updated++
					changed = true
				}
			}
		}
		for id := range gone {
			if ks := ids[id]; strings.HasPrefix(id, "plugin|") && len(ks) == 1 && hashOf(id) == seen[id] {
				delete(m, ks[0])
				changed = true
			}
		}
		if ownersChanged {
			if err := writeLogins(ls); err != nil {
				return false, err
			}
		}
		return changed, nil
	})
}

// signCodexIn signs Codex in to l where it is signed out: auth.json (or
// the store its config.toml names) gets l's sign-in, written beside the
// old file and renamed in. Codex signed in, its sign-in unreadable, or
// its account removed in magpie: nothing is written.
func signCodexIn(l savedLogin) (bool, error) {
	if l.Agent != "codex" || len(l.Auth) == 0 || accountRemoved("codex") {
		return false, nil
	}
	home := codexHome()
	if _, _, err := readCodexLogin(home, false); !errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err := saveCodexLogin(home, l.Auth); err != nil {
		return false, err
	}
	return true, nil
}
