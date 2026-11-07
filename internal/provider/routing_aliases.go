package provider

import (
	"errors"
	"fmt"
	"maps"
	"regexp"
	"strings"
	"sync"
)

// Alias bindings retain their owner after retirement so old references cannot
// silently select a different account. Key rotation changes KeyID alone.
type AccountAliasBinding struct {
	AccountID string `json:"accountId"`
	Active    bool   `json:"active"`
}
type KeyAliasBinding struct {
	OwnerID string `json:"ownerId"`
	KeyID   string `json:"keyId"`
	Active  bool   `json:"active"`
}
type routingSelection struct{ alias, account, owner string }

var routingAliasPattern = regexp.MustCompile(`^[a-z][a-z0-9._-]{0,31}$`)
var nativeAtNumber = regexp.MustCompile(`^[0-9]+[kKmM]?$`)
var routingAliasMu sync.Mutex

// RoutingError carries the HTTP classification for a model reference rejected
// before dispatch. It contains no credentials.
type RoutingError struct {
	Status      int
	Message     string
	aliasOrigin bool
}

func (e *RoutingError) Error() string { return e.Message }

// IsRoutingAliasError reports a refusal while resolving an account qualifier.
// An ordinary unknown model keeps its HTTP classification but is not one.
func IsRoutingAliasError(err error) bool {
	var e *RoutingError
	return errors.As(err, &e) && e.aliasOrigin
}

func aliasError(status int, format string, args ...any) error {
	return &RoutingError{Status: status, Message: fmt.Sprintf(format, args...)}
}

func (p Provider) RoutingAlias() string     { return p.routing.alias }
func (p Provider) RoutingAccountID() string { return p.routing.account }
func (p Provider) ModelRef(model string) string {
	ref := p.ID + "/" + model
	if p.routing.alias != "" {
		ref += "@" + p.routing.alias
	}
	return ref
}

// BaseModelRef is ref with its routing-alias qualifier ("p/m@work")
// taken off: the base ref a call of it is recorded by (usage's Provider
// and Model), whichever account the alias picked. A ref that names no
// alias serving now is itself: unqualified, a native id that keeps its
// own "@" (a number marker), or an alias that is off, retired or gone,
// whose requests fail resolution on their own.
func BaseModelRef(ref string) string {
	p, m, ok := Resolve(ref)
	if !ok || p.RoutingAlias() == "" {
		return ref
	}
	return p.ID + "/" + m
}

// RoutingID uses the saved plugin slot's identity, whose label may change.
// AccountID remains the existing gateway-key identity for compatibility.
func (p Provider) RoutingID() string {
	if p.IsPlugin() {
		return p.pluginRoutingID(p.Account.pluginKey)
	}
	return p.AccountID()
}

func (p Provider) pluginRoutingID(slot string) string {
	for _, l := range readLogins() {
		if l.Agent == pluginAgent(*p.Account.plugin) && l.Home == slot {
			return loginID(l)
		}
	}
	return ""
}

func (p Provider) RoutingIDOf(user string) string {
	if p.IsPlugin() {
		if user == p.Account.User {
			return p.RoutingID()
		}
		for slot, label := range pluginNames(*p.Account.plugin) {
			if accountKey(label) == accountKey(user) {
				return p.pluginRoutingID(slot)
			}
		}
		return ""
	}
	if p.Account != nil {
		return LoginID(p.Account.Agent, user)
	}
	return user
}
func (p Provider) ActiveRoutingAlias(id string) string {
	for name, b := range p.AccountAliases {
		if b.Active && b.AccountID == id {
			return name
		}
	}
	for name, b := range p.KeyAliases {
		if b.Active && b.KeyID == id {
			return name
		}
	}
	return ""
}
func (p Provider) HasRoutingAliases() bool {
	return len(p.AccountAliases)+len(p.KeyAliases) > 0 || p.RoutingAlias() != ""
}
func (p Provider) RoutingAllows(id string) bool {
	if validateRoutingAliases(p) != nil {
		return false
	}
	if p.routing.alias != "" {
		return id == p.routing.account
	}
	return p.ActiveRoutingAlias(id) == ""
}

// RoutingCurrent rechecks a held candidate against current storage just before
// dispatch. A retired or renamed selection never falls back to the ordinary pool.
func (p Provider) RoutingCurrent() bool {
	state := readProviderState()
	if state.err != nil {
		return false
	}
	f := state.file
	current := Provider{ID: p.ID}
	for _, q := range f.Providers {
		if q.ID == p.ID {
			current = q
			break
		}
	}
	current.routing = p.routing
	if p.routing.alias != "" {
		if b, ok := current.AccountAliases[p.routing.alias]; ok {
			if !b.Active || b.AccountID != p.routing.account {
				return false
			}
		} else if b, ok := current.KeyAliases[p.routing.alias]; ok {
			if !b.Active || b.KeyID != p.routing.account || b.OwnerID != p.routing.owner {
				return false
			}
		} else {
			return false
		}
	}
	return current.RoutingAllows(p.RoutingID())
}
func validateRoutingAliases(p Provider) error {
	active := map[string]bool{}
	keys := map[string]string{}
	owners := map[string]string{}
	for name, b := range p.AccountAliases {
		if !routingAliasPattern.MatchString(name) || b.AccountID == "" {
			return fmt.Errorf("%s: invalid account routing alias %q", p.ID, name)
		}
		if _, ok := p.KeyAliases[name]; ok {
			return fmt.Errorf("%s: duplicate routing alias %q", p.ID, name)
		}
		if b.Active {
			if active["a:"+b.AccountID] {
				return fmt.Errorf("%s: multiple active aliases for one account", p.ID)
			}
			active["a:"+b.AccountID] = true
		}
	}
	for name, b := range p.KeyAliases {
		if !routingAliasPattern.MatchString(name) || b.OwnerID == "" || b.KeyID == "" {
			return fmt.Errorf("%s: invalid key routing alias %q", p.ID, name)
		}
		if old, ok := owners[b.OwnerID]; ok && old != b.KeyID {
			return fmt.Errorf("%s: inconsistent routing alias key owner", p.ID)
		}
		owners[b.OwnerID] = b.KeyID
		if old, ok := keys[b.KeyID]; ok && old != b.OwnerID {
			return fmt.Errorf("%s: conflicting routing alias key owners", p.ID)
		}
		keys[b.KeyID] = b.OwnerID
		if b.Active {
			if active["k:"+b.OwnerID] {
				return fmt.Errorf("%s: multiple active aliases for one key", p.ID)
			}
			active["k:"+b.OwnerID] = true
		}
	}
	return nil
}

// routingAliasTail identifies only a final suffix after the last path slash.
// The native base keeps its spelling; both setter and resolver normalize only
// the suffix so a new alias cannot capture an existing cased native ID.
func routingAliasTail(model string) (int, string) {
	at := strings.LastIndex(model, "@")
	if at < 0 || at < strings.LastIndex(model, "/") {
		return -1, ""
	}
	return at, strings.ToLower(model[at+1:])
}

func SetAccountRoutingAlias(id, account, alias string) error {
	return setRoutingAlias(id, account, alias, false)
}
func SetKeyRoutingAlias(id, key, alias string) error { return setRoutingAlias(id, key, alias, true) }
func setRoutingAlias(id, target, alias string, key bool) error {
	routingAliasMu.Lock()
	defer routingAliasMu.Unlock()
	state := readProviderState()
	if state.err != nil {
		return state.err
	}
	f := state.file
	p, err := Find(id)
	if err != nil {
		return err
	}
	if err = validateRoutingAliases(*p); err != nil {
		return err
	}
	alias = strings.ToLower(strings.TrimSpace(alias))
	if alias != "" && !routingAliasPattern.MatchString(alias) {
		return aliasError(400, "routing alias must be 1–32 ASCII letters, digits, dots, underscores or dashes, starting with a letter")
	}
	found := false
	if key {
		for _, k := range p.KeyList() {
			if k.ID == target {
				found = true
			}
		}
	} else {
		for _, a := range p.AccountIDs() {
			if p.RoutingIDOf(a.User) == target {
				found = true
			}
		}
	}
	if !found {
		return aliasError(404, "%s has no such routing account or key", id)
	}
	if alias != "" {
		for _, m := range p.Available() {
			if _, suffix := routingAliasTail(m.ID); suffix == alias {
				return aliasError(409, "routing alias %q conflicts with native model %q", alias, m.ID)
			}
		}
		for _, m := range p.Models {
			if _, suffix := routingAliasTail(m); suffix == alias {
				return aliasError(409, "routing alias %q conflicts with native model %q", alias, m)
			}
		}
	}
	p.AccountAliases = maps.Clone(p.AccountAliases)
	p.KeyAliases = maps.Clone(p.KeyAliases)
	if key {
		owner := target
		for _, b := range p.KeyAliases {
			if b.KeyID == target {
				owner = b.OwnerID
				break
			}
		}
		if _, ok := p.AccountAliases[alias]; alias != "" && ok {
			return aliasError(409, "routing alias %q belongs to another account", alias)
		}
		if b, ok := p.KeyAliases[alias]; alias != "" && ok && b.OwnerID != owner {
			return aliasError(409, "routing alias %q belongs to another key", alias)
		}
		for name, b := range p.KeyAliases {
			if b.OwnerID == owner {
				b.Active = false
				p.KeyAliases[name] = b
			}
		}
		if alias != "" {
			if p.KeyAliases == nil {
				p.KeyAliases = map[string]KeyAliasBinding{}
			}
			p.KeyAliases[alias] = KeyAliasBinding{owner, target, true}
		}
	} else {
		if _, ok := p.KeyAliases[alias]; alias != "" && ok {
			return aliasError(409, "routing alias %q belongs to another key", alias)
		}
		if b, ok := p.AccountAliases[alias]; alias != "" && ok && b.AccountID != target {
			return aliasError(409, "routing alias %q belongs to another account", alias)
		}
		for name, b := range p.AccountAliases {
			if b.AccountID == target {
				b.Active = false
				p.AccountAliases[name] = b
			}
		}
		if alias != "" {
			if p.AccountAliases == nil {
				p.AccountAliases = map[string]AccountAliasBinding{}
			}
			p.AccountAliases[alias] = AccountAliasBinding{target, true}
		}
	}
	// Only registry metadata is edited: account credentials and all other picks
	// remain byte-for-byte values from the stored provider record.
	for i, q := range f.Providers {
		if q.ID == p.ID {
			f.Providers[i].AccountAliases = p.AccountAliases
			f.Providers[i].KeyAliases = p.KeyAliases
			return store(f)
		}
	}
	f.Providers = append(f.Providers, Provider{ID: p.ID, AccountAliases: p.AccountAliases, KeyAliases: p.KeyAliases})
	return store(f)
}

// resolveRoutingAlias distinguishes literal native IDs from account qualifiers.
// handled is true for errors too, so permissive resolution cannot undo a refusal.
func resolveRoutingAlias(id string) (p Provider, model string, handled bool, err error) {
	defer func() {
		if e, ok := err.(*RoutingError); handled && ok {
			e.aliasOrigin = true
		}
	}()
	pid, model, ok := strings.Cut(id, "/")
	if !ok {
		return p, "", false, nil
	}
	at, alias := routingAliasTail(model)
	if at < 0 {
		return p, "", false, nil
	}
	suffix := model[at+1:]
	q := lookIn(All(), pid)
	if q == nil {
		if !nativeAtNumber.MatchString(suffix) {
			return p, "", true, aliasError(404, "routing alias provider %q is unavailable", pid)
		}
		return p, "", false, nil
	}
	p = *q
	_, accountReserved := p.AccountAliases[alias]
	_, keyReserved := p.KeyAliases[alias]
	known := false
	for _, m := range p.Available() {
		if m.ID == model {
			known = true
			break
		}
	}
	for _, m := range p.Models {
		if m == model {
			known = true
			break
		}
	}
	if known {
		if accountReserved || keyReserved {
			return p, "", true, aliasError(409, "routing alias %q conflicts with a native model", alias)
		}
		return p, "", false, nil
	}
	if nativeAtNumber.MatchString(suffix) {
		return p, "", false, nil
	}
	if !routingAliasPattern.MatchString(alias) {
		return p, "", true, aliasError(400, "invalid routing alias %q: use 1–32 ASCII characters starting with a letter", suffix)
	}
	if err = validateRoutingAliases(p); err != nil {
		return p, "", true, err
	}
	if !p.On() {
		return p, "", true, aliasError(404, "routing alias provider %q is switched off", pid)
	}
	if p.ServesNone() {
		return p, "", true, aliasError(404, "routing alias provider %q serves no models (usage only)", pid)
	}
	sel := routingSelection{alias: alias}
	if b, ok := p.AccountAliases[alias]; ok {
		if !b.Active {
			return p, "", true, aliasError(404, "routing alias %q is retired", alias)
		}
		sel.account = b.AccountID
	} else if b, ok := p.KeyAliases[alias]; ok {
		if !b.Active {
			return p, "", true, aliasError(404, "routing alias %q is retired", alias)
		}
		sel.account, sel.owner = b.KeyID, b.OwnerID
	} else {
		return p, "", true, aliasError(404, "unknown routing alias %q", alias)
	}
	available := false
	if p.Account != nil {
		for _, a := range append([]Provider{p}, p.AlsoOn()...) {
			if a.RoutingID() == sel.account {
				available = true
			}
		}
	} else {
		for _, k := range p.KeysOn() {
			if KeyID(k.Key) == sel.account {
				available = true
			}
		}
	}
	if !available {
		return p, "", true, aliasError(404, "routing alias %q has no available account", alias)
	}
	p.routing = sel
	return p, model[:at], true, nil
}

// ResolveModel preserves explicit alias failures instead of accepting the suffix
// as an arbitrary upstream model ID.
func ResolveModel(id string) (Provider, string, error) {
	id = strings.TrimSuffix(strings.TrimSpace(id), "[1m]")
	if err := RoutingFileError(); err != nil {
		return Provider{}, "", err
	}
	base, _ := MemberEffort(id)
	if p, m, handled, err := resolveRoutingAlias(base); handled {
		return p, m, err
	}
	p, m, ok := Resolve(id)
	if !ok {
		return p, m, aliasError(404, "unknown model %q", id)
	}
	return p, m, nil
}

func qualifiedEntries(base []Entry) []Entry {
	var out []Entry
	for _, e := range base {
		if e.Group != "" {
			continue
		}
		p := e.Provider
		aliases := map[string]bool{}
		for name, b := range p.AccountAliases {
			if b.Active {
				aliases[name] = true
			}
		}
		for name, b := range p.KeyAliases {
			if b.Active {
				aliases[name] = true
			}
		}
		for name := range aliases {
			q, m, _, err := resolveRoutingAlias(e.ID + "@" + name)
			if err != nil {
				continue
			}
			serves := false
			if q.Account != nil {
				for _, a := range append([]Provider{q}, q.AlsoOn()...) {
					if a.RoutingID() == q.routing.account && a.AccountServes(a.Account.User, m) && a.Account.Lists(m) {
						serves = true
					}
				}
			} else {
				for _, k := range q.KeysOn() {
					if KeyID(k.Key) == q.routing.account && q.AccountServes(KeyID(k.Key), m) && q.PlanServing(m)(k) && q.Serving(m)(k) {
						serves = true
					}
				}
			}
			if !serves {
				continue
			}
			entry := e
			entry.ID = q.ModelRef(m)
			entry.BaseID = e.ID
			entry.RoutingAlias = name
			entry.Provider = &q
			out = append(out, entry)
		}
	}
	return out
}

// mergeRoutingHistory validates an incoming registry before any restore writes.
// Entries absent from the selected version survive as retired reservations.
func mergeRoutingHistory(in *Provider, here Provider) error {
	in.AccountAliases = maps.Clone(in.AccountAliases)
	in.KeyAliases = maps.Clone(in.KeyAliases)
	for name, b := range here.AccountAliases {
		if _, ok := in.KeyAliases[name]; ok {
			return fmt.Errorf("routing alias %q owner conflict", name)
		}
		if next, ok := in.AccountAliases[name]; ok {
			if next.AccountID != b.AccountID {
				return fmt.Errorf("routing alias %q owner conflict", name)
			}
			continue
		}
		if in.AccountAliases == nil {
			in.AccountAliases = map[string]AccountAliasBinding{}
		}
		b.Active = false
		in.AccountAliases[name] = b
	}
	for name, b := range here.KeyAliases {
		if _, ok := in.AccountAliases[name]; ok {
			return fmt.Errorf("routing alias %q owner conflict", name)
		}
		if next, ok := in.KeyAliases[name]; ok {
			if next.OwnerID != b.OwnerID {
				return fmt.Errorf("routing alias %q owner conflict", name)
			}
			continue
		}
		// Historical aliases of an owner rotated by the incoming version follow
		// that owner's current key, while preserving the original OwnerID.
		for _, next := range in.KeyAliases {
			if next.OwnerID == b.OwnerID {
				b.KeyID = next.KeyID
				break
			}
		}
		if in.KeyAliases == nil {
			in.KeyAliases = map[string]KeyAliasBinding{}
		}
		b.Active = false
		in.KeyAliases[name] = b
	}
	return validateRoutingAliases(*in)
}
