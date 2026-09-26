//go:build integration

package integration

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/MurilojrMarques/backend-challenge-go/internal/adapters/httpapi"
	"github.com/MurilojrMarques/backend-challenge-go/internal/adapters/postgres"
	"github.com/MurilojrMarques/backend-challenge-go/internal/application"
	"github.com/MurilojrMarques/backend-challenge-go/internal/config"
	"github.com/MurilojrMarques/backend-challenge-go/test/testutil"
)

func fetchToken(t *testing.T, clientID string) string {
	t.Helper()
	token, err := testutil.Token(context.Background(), kc.URL, clientID)
	require.NoError(t, err)
	return token
}

func startedVerifier(t *testing.T) *httpapi.OIDCVerifier {
	t.Helper()
	v := httpapi.NewOIDCVerifier(config.OIDC{Issuer: kc.Issuer(), Audience: testutil.Audience})
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	require.NoError(t, v.Start(ctx))
	return v
}

func TestOIDCVerifierAgainstKeycloak(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	v := startedVerifier(t)
	shortLived := fetchToken(t, testutil.ShortLivedClient)
	issued := time.Now()

	claims, err := v.Verify(ctx, fetchToken(t, testutil.ProviderAClient))
	require.NoError(t, err)
	assert.Contains(t, claims.Roles, httpapi.RoleProvider)
	assert.Equal(t, testutil.ProviderAClient, claims.ProviderID)
	assert.Equal(t, testutil.ProviderAClient, claims.ClientID)
	principal, err := httpapi.PrincipalFromClaims(claims)
	require.NoError(t, err)
	assert.Equal(t, application.RoleProvider, principal.Role)
	assert.Equal(t, testutil.ProviderAClient, principal.ProviderID)

	claims, err = v.Verify(ctx, fetchToken(t, testutil.InternalClient))
	require.NoError(t, err)
	assert.Contains(t, claims.Roles, httpapi.RoleInternal)
	principal, err = httpapi.PrincipalFromClaims(claims)
	require.NoError(t, err)
	assert.Equal(t, application.RoleInternal, principal.Role)

	claims, err = v.Verify(ctx, fetchToken(t, testutil.NoRoleClient))
	require.NoError(t, err, "the token is genuine")
	_, err = httpapi.PrincipalFromClaims(claims)
	assert.ErrorIs(t, err, application.ErrForbidden, "but carries no wallet role")

	genuine := fetchToken(t, testutil.ProviderAClient)
	_, err = v.Verify(ctx, genuine[:len(genuine)-4]+"AAAA")
	assert.ErrorIs(t, err, httpapi.ErrUnauthenticated, "a tampered signature is rejected")

	claims, err = v.Verify(ctx, shortLived)
	require.NoError(t, err)
	assert.Equal(t, testutil.ProviderAClient, claims.ProviderID)

	time.Sleep(time.Until(issued.Add(38 * time.Second)))
	_, err = v.Verify(ctx, shortLived)
	assert.ErrorIs(t, err, httpapi.ErrUnauthenticated, "a five second token is expired once the 30s leeway is over")
}

func TestHTTPStackWithRealTokens(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newServices(t)
	handler, err := httpapi.NewRouter(httpapi.Deps{
		Options:  httpapi.Options{EnableAPI: true},
		Logger:   discard,
		Verifier: startedVerifier(t),
		Wallets:  s.wallets,
		Wagers:   s.wagers,
		Health:   []application.HealthChecker{postgres.NewHealth(s.pool)},
	})
	require.NoError(t, err)
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	anonymous := testutil.NewClient(srv.URL, "")
	internalAPI := testutil.NewClient(srv.URL, fetchToken(t, testutil.InternalClient))
	providerA := testutil.NewClient(srv.URL, fetchToken(t, testutil.ProviderAClient))
	providerB := testutil.NewClient(srv.URL, fetchToken(t, testutil.ProviderBClient))

	res, err := anonymous.Get(ctx, "/health/ready")
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, res.Status, string(res.Raw))

	player := newID().String()
	res, err = internalAPI.OpenWallet(ctx, player, "100.00")
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, res.Status, string(res.Raw))
	walletID := res.String("id")

	res, err = providerA.OpenWallet(ctx, player, "100.00")
	require.NoError(t, err)
	assert.Equal(t, http.StatusForbidden, res.Status)
	res, err = anonymous.OpenWallet(ctx, player, "100.00")
	require.NoError(t, err)
	assert.Equal(t, http.StatusUnauthorized, res.Status)
	assert.Equal(t, `Bearer realm="wallet"`, res.Header.Get("WWW-Authenticate"))

	bet := testutil.NewWager(walletID, player, "bet-"+newID().String(), "BET", "25.00")
	res, err = providerA.Submit(ctx, bet, bet.Key())
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, res.Status, string(res.Raw))
	assert.Equal(t, "PROCESSED", res.String("status"))
	assert.Equal(t, "75.00", res.Amount("balance"))

	res, err = providerB.Submit(ctx, bet, bet.Key())
	require.NoError(t, err)
	assert.Equal(t, http.StatusForbidden, res.Status, "a provider token only acts for its own providerId")

	res, err = providerB.Get(ctx, "/providers/provider-a/wagering/transactions/"+bet.ExternalTransactionID)
	require.NoError(t, err)
	assert.Equal(t, http.StatusNotFound, res.Status)
	res, err = providerA.Get(ctx, "/providers/provider-a/wagering/transactions/"+bet.ExternalTransactionID)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, res.Status)
}
