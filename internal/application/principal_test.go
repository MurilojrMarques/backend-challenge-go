package application_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/MurilojrMarques/backend-challenge-go/internal/application"
)

func TestPrincipalPolicies(t *testing.T) {
	t.Parallel()

	internal := application.Principal{Subject: "svc", Role: application.RoleInternal}
	providerA := application.Principal{Subject: "a", Role: application.RoleProvider, ProviderID: "provider-a"}

	assert.NoError(t, internal.RequireInternal())
	assert.ErrorIs(t, providerA.RequireInternal(), application.ErrForbidden)

	assert.NoError(t, providerA.RequireProvider("provider-a"))
	assert.ErrorIs(t, providerA.RequireProvider("provider-b"), application.ErrForbidden)
	assert.ErrorIs(t, internal.RequireProvider("provider-a"), application.ErrForbidden, "internal service does not impersonate providers")

	invalid := []application.Principal{
		{},
		{Subject: "x", Role: "admin"},
		{Subject: "x", Role: application.RoleProvider},
		{Role: application.RoleInternal},
	}
	for _, p := range invalid {
		assert.ErrorIs(t, p.Validate(), application.ErrForbidden, "%+v", p)
	}
}

func TestPrincipalContext(t *testing.T) {
	t.Parallel()

	_, ok := application.PrincipalFrom(context.Background())
	assert.False(t, ok)

	p := application.Principal{Subject: "a", Role: application.RoleProvider, ProviderID: "provider-a"}
	ctx := application.WithPrincipal(context.Background(), p)
	got, ok := application.PrincipalFrom(ctx)
	assert.True(t, ok)
	assert.Equal(t, p, got)
}
