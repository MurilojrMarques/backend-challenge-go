package application

import (
	"context"
	"fmt"
)

type Role string

const (
	RoleInternal Role = "internal"
	RoleProvider Role = "provider"
)

type Principal struct {
	Subject    string
	Role       Role
	ProviderID string
}

func (p Principal) Validate() error {
	if p.Subject == "" {
		return fmt.Errorf("%w: principal without subject", ErrForbidden)
	}
	switch p.Role {
	case RoleInternal:
		return nil
	case RoleProvider:
		if p.ProviderID == "" {
			return fmt.Errorf("%w: provider principal without providerId", ErrForbidden)
		}
		return nil
	default:
		return fmt.Errorf("%w: unknown role %q", ErrForbidden, p.Role)
	}
}

func (p Principal) IsInternal() bool {
	return p.Role == RoleInternal
}

func (p Principal) CanActAsProvider(providerID string) bool {
	return p.Role == RoleProvider && p.ProviderID == providerID
}

func (p Principal) RequireInternal() error {
	if err := p.Validate(); err != nil {
		return err
	}
	if !p.IsInternal() {
		return fmt.Errorf("%w: internal role required", ErrForbidden)
	}
	return nil
}

func (p Principal) RequireProvider(providerID string) error {
	if err := p.Validate(); err != nil {
		return err
	}
	if !p.CanActAsProvider(providerID) {
		return fmt.Errorf("%w: principal cannot act as provider %q", ErrForbidden, providerID)
	}
	return nil
}

type principalKey struct{}

func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, p)
}

func PrincipalFrom(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(Principal)
	return p, ok
}
