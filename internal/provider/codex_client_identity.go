package provider

import (
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strings"

	"github.com/yetone/magpie/internal/filememo"
)

// CodexClientIdentity matches the client's credential to a kept Codex sign-in,
// including saved accounts that are off. It never refreshes a token. Unknown
// credentials are distinct from unreadable stores or conflicting account IDs.
// A miss is checked against fresh metadata before it is classified; this reads
// the stores again without asking the vendor to renew any credential.
func CodexClientIdentity(token, accountID string) (AccountRef, bool, error) {
	ref, managed, err := codexClientIdentity(token, accountID, false)
	if managed || err != nil {
		return ref, managed, err
	}
	return codexClientIdentity(token, accountID, true)
}

func codexClientIdentity(token, accountID string, fresh bool) (AccountRef, bool, error) {
	if discoveryOff() {
		return AccountRef{}, false, errAccountDiscoveryOff
	}
	var ls []savedLogin
	var err error
	if fresh {
		ls, err = filememo.ReadNow("logins", loginsPath(), parseLogins)
	} else {
		ls, err = filememo.Read("logins", loginsPath(), parseLogins)
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return AccountRef{}, false, errors.New("cannot read saved Codex identities")
	}
	type kept struct {
		ref       AccountRef
		token, id string
	}
	var identities []kept
	add := func(raw []byte, user string) error {
		var a codexAuth
		if json.Unmarshal(raw, &a) != nil {
			return errors.New("cannot parse a kept Codex identity")
		}
		if a.AuthMode == "apikey" || a.Tokens.AccessToken == "" && codexAccountID(a) == "" {
			return nil
		}
		if user == "" {
			user = codexUser(jwtClaims(a.Tokens.IDToken))
			if user == "" {
				user = codexAccountID(a)
			}
			user = codexName(ls, savedLogin{Agent: "codex", User: user, Auth: raw})
		}
		if user == "" {
			return errors.New("kept Codex identity has no account name")
		}
		ref := AccountRef{ID: loginID(savedLogin{Agent: "codex", User: user}), User: user}
		for _, l := range ls {
			if l.Agent == "codex" && strings.EqualFold(l.User, user) {
				ref.ID = loginID(l)
				break
			}
		}
		identities = append(identities, kept{ref: ref, token: a.Tokens.AccessToken, id: codexAccountID(a)})
		return nil
	}
	homes := append([]string{codexHome()}, codexOtherHomes()...)
	for _, l := range ls {
		if l.Agent != "codex" {
			continue
		}
		if len(l.Auth) > 0 {
			if err := add(l.Auth, l.User); err != nil {
				return AccountRef{}, false, err
			}
		}
		if l.Home != "" && !slices.Contains(homes, l.Home) {
			homes = append(homes, l.Home)
		}
	}
	for _, home := range homes {
		raw, _, err := readCodexLogin(home, fresh)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return AccountRef{}, false, errors.New("cannot read a Codex sign-in identity")
		}
		if err := add(raw, ""); err != nil {
			return AccountRef{}, false, err
		}
	}
	var match AccountRef
	knownID, knownToken := false, false
	for _, k := range identities {
		knownID = knownID || accountID != "" && accountID == k.id
		if token == "" || token != k.token {
			continue
		}
		knownToken = true
		if accountID != "" && accountID != k.id {
			continue
		}
		if match.ID != "" && match.ID != k.ref.ID {
			return AccountRef{}, false, errors.New("Codex credential has conflicting saved owners")
		}
		match = k.ref
	}
	if match.ID != "" {
		return match, true, nil
	}
	if fresh && (knownID || knownToken) {
		return AccountRef{}, false, errors.New("Codex credential does not match the kept account")
	}
	return AccountRef{}, false, nil
}
