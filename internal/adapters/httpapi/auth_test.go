package httpapi_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/MurilojrMarques/backend-challenge-go/internal/adapters/httpapi"
	"github.com/MurilojrMarques/backend-challenge-go/internal/application"
	"github.com/MurilojrMarques/backend-challenge-go/internal/config"
)

const audience = "wallet-api"

type issuer struct {
	srv      *httptest.Server
	key      *rsa.PrivateKey
	jwksDown atomic.Bool
}

func newIssuer(t *testing.T) *issuer {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	iss := &issuer{key: key}

	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                                iss.srv.URL,
			"jwks_uri":                              iss.srv.URL + "/keys",
			"authorization_endpoint":                iss.srv.URL + "/auth",
			"token_endpoint":                        iss.srv.URL + "/token",
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	mux.HandleFunc("/keys", func(w http.ResponseWriter, _ *http.Request) {
		if iss.jwksDown.Load() {
			http.Error(w, "jwks unavailable", http.StatusServiceUnavailable)
			return
		}
		_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &key.PublicKey, KeyID: "k1", Algorithm: "RS256", Use: "sig"}}})
	})
	iss.srv = httptest.NewServer(mux)
	t.Cleanup(iss.srv.Close)
	return iss
}

type tokenOptions struct {
	audience string
	expiry   time.Time
	key      any
	kid      string
	alg      jose.SignatureAlgorithm
	private  map[string]any
}

func (iss *issuer) token(t *testing.T, opts tokenOptions) string {
	t.Helper()
	if opts.audience == "" {
		opts.audience = audience
	}
	if opts.expiry.IsZero() {
		opts.expiry = time.Now().Add(5 * time.Minute)
	}
	if opts.key == nil {
		opts.key, opts.kid, opts.alg = iss.key, "k1", jose.RS256
	}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: opts.alg, Key: opts.key}, (&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", opts.kid))
	require.NoError(t, err)
	claims := jwt.Claims{
		Issuer:   iss.srv.URL,
		Subject:  "service-account-provider-a",
		Audience: jwt.Audience{opts.audience},
		Expiry:   jwt.NewNumericDate(opts.expiry),
		IssuedAt: jwt.NewNumericDate(time.Now().Add(-time.Minute)),
	}
	private := map[string]any{
		"azp":          "provider-a",
		"providerId":   "provider-a",
		"realm_access": map[string]any{"roles": []string{httpapi.RoleProvider}},
	}
	for k, v := range opts.private {
		private[k] = v
	}
	raw, err := jwt.Signed(signer).Claims(claims).Claims(private).Serialize()
	require.NoError(t, err)
	return raw
}

func startedVerifier(t *testing.T, iss *issuer) *httpapi.OIDCVerifier {
	t.Helper()
	v := httpapi.NewOIDCVerifier(config.OIDC{Issuer: iss.srv.URL, Audience: audience})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, v.Start(ctx))
	return v
}

func TestOIDCVerifierAcceptsTokensFromTheIssuer(t *testing.T) {
	t.Parallel()
	iss := newIssuer(t)
	v := startedVerifier(t, iss)

	claims, err := v.Verify(context.Background(), iss.token(t, tokenOptions{}))
	require.NoError(t, err)
	assert.Equal(t, "service-account-provider-a", claims.Subject)
	assert.Equal(t, "provider-a", claims.ClientID)
	assert.Equal(t, "provider-a", claims.ProviderID)
	assert.Equal(t, []string{httpapi.RoleProvider}, claims.Roles)

	principal, err := httpapi.PrincipalFromClaims(claims)
	require.NoError(t, err)
	assert.Equal(t, application.RoleProvider, principal.Role)
	assert.Equal(t, "provider-a", principal.ProviderID)

	recent, err := v.Verify(context.Background(), iss.token(t, tokenOptions{expiry: time.Now().Add(-10 * time.Second)}))
	require.NoError(t, err, "a few seconds of clock skew are tolerated")
	assert.Equal(t, "provider-a", recent.ProviderID)
}

func TestOIDCVerifierRejectsBadTokens(t *testing.T) {
	t.Parallel()
	iss := newIssuer(t)
	v := startedVerifier(t, iss)

	otherKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	cases := map[string]string{
		"wrong audience": iss.token(t, tokenOptions{audience: "another-api"}),
		"expired":        iss.token(t, tokenOptions{expiry: time.Now().Add(-2 * time.Minute)}),
		"hmac signed":    iss.token(t, tokenOptions{key: []byte("0123456789abcdef0123456789abcdef"), kid: "k1", alg: jose.HS256}),
		"unknown key":    iss.token(t, tokenOptions{key: otherKey, kid: "k2", alg: jose.RS256}),
		"garbage":        "not.a.jwt",
	}
	for name, raw := range cases {
		_, err := v.Verify(context.Background(), raw)
		assert.ErrorIs(t, err, httpapi.ErrUnauthenticated, name)
	}
}

func TestOIDCVerifierReportsKeyFetchFailuresAsNotReady(t *testing.T) {
	t.Parallel()
	iss := newIssuer(t)
	v := startedVerifier(t, iss)

	rotated, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	iss.jwksDown.Store(true)
	_, err = v.Verify(context.Background(), iss.token(t, tokenOptions{key: rotated, kid: "k2", alg: jose.RS256}))
	assert.ErrorIs(t, err, httpapi.ErrAuthNotReady, "an unreachable JWKS is a dependency failure, not a bad credential")
	assert.NotErrorIs(t, err, httpapi.ErrUnauthenticated)
}

func TestOIDCVerifierBeforeDiscovery(t *testing.T) {
	t.Parallel()
	iss := newIssuer(t)
	v := httpapi.NewOIDCVerifier(config.OIDC{Issuer: iss.srv.URL, Audience: audience})
	_, err := v.Verify(context.Background(), iss.token(t, tokenOptions{}))
	assert.ErrorIs(t, err, httpapi.ErrAuthNotReady)

	iss.srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	assert.ErrorIs(t, v.Start(ctx), application.ErrUnavailable, "discovery keeps retrying until the start deadline")
}
