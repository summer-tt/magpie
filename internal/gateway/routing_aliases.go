package gateway

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/yetone/magpie/internal/provider"
)

type routingConstraintKey struct{}
type routingConstraint struct{ ref, provider, account string }

func routingStatus(err error) int {
	var e *provider.RoutingError
	if errors.As(err, &e) {
		return e.Status
	}
	return http.StatusServiceUnavailable
}

// constrainRouting runs before middleware and automatic side-model replacement.
// The original reference remains authoritative until the request is dispatched.
func constrainRouting(w http.ResponseWriter, r *http.Request, from provider.Protocol, body []byte) (*http.Request, bool) {
	if r.Context().Value(routingConstraintKey{}) != nil {
		return r, true
	}
	ref := modelOf(body)
	p, _, err := provider.ResolveModel(ref)
	if err != nil {
		// Preserve existing unknown-model handling for ordinary references.
		if provider.RoutingFileError() != nil || hasAliasIntent(ref) {
			writeError(w, from, routingStatus(err), err.Error())
			return r, false
		}
		return r, true
	}
	if p.RoutingAlias() == "" {
		return r, true
	}
	c := routingConstraint{ref, p.ID, p.RoutingAccountID()}
	return r.WithContext(context.WithValue(r.Context(), routingConstraintKey{}, c)), true
}
func hasAliasIntent(ref string) bool {
	// The resolver owns native @ interpretation; this is used only after it
	// rejected a reference, never to split or modify a native model.
	for i := len(ref) - 1; i >= 0; i-- {
		if ref[i] == '/' {
			return false
		}
		if ref[i] == '@' {
			return true
		}
	}
	return false
}
func constrainedRef(r *http.Request) string {
	c, _ := r.Context().Value(routingConstraintKey{}).(routingConstraint)
	return c.ref
}
func routingRewriteAllowed(r *http.Request, body []byte) bool {
	c, ok := r.Context().Value(routingConstraintKey{}).(routingConstraint)
	if !ok {
		return true
	}
	p, _, err := provider.ResolveModel(modelOf(body))
	return err == nil && p.ID == c.provider && p.RoutingAccountID() == c.account && p.RoutingAlias() != ""
}

// routingSendKey binds one generation request to its selected account. Resource
// GETs do not carry it: they keep the credential that created the resource.
type routingSendKey struct{}

func routingRequest(req *http.Request, p provider.Provider) *http.Request {
	return req.WithContext(context.WithValue(req.Context(), routingSendKey{}, p))
}

func routingSendError(p provider.Provider) error {
	if !p.RoutingCurrent() {
		return fmt.Errorf("routing account is no longer available for %s", p.ID)
	}
	return nil
}
