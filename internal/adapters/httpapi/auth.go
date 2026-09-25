package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"

	"github.com/MurilojrMarques/backend-challenge-go/internal/application"
	"github.com/MurilojrMarques/backend-challenge-go/internal/config"
)

const (
	RoleInternal = "wallet-internal"
	RoleProvider = "wallet-provider"
)

var (
	ErrUnauthenticated = errors.New("httpapi: unauthenticated")
	ErrAuthNotReady    = errors.New("httpapi: authenticator not ready")
)

type Claims struct {
	Subject    string
	ClientID   string
	ProviderID string
	Roles      []string
}

type TokenVerifier interface {
	Verify(ctx context.Context, rawToken string) (Claims, error)
}

func PrincipalFromClaims(c Claims) (application.Principal, error) {
	subject := c.Subject
	if subject == "" {
		subject = c.ClientID
	}
	switch {
	case slices.Contains(c.Roles, RoleInternal):
		return application.Principal{Subject: subject, Role: application.RoleInternal}, nil
	case slices.Contains(c.Roles, RoleProvider):
		if c.ProviderID == "" {
			return application.Principal{}, fmt.Errorf("%w: provider token without providerId claim", application.ErrForbidden)
		}
		return application.Principal{Subject: subject, Role: application.RoleProvider, ProviderID: c.ProviderID}, nil
	default:
		return application.Principal{}, fmt.Errorf("%w: token carries no wallet role", application.ErrForbidden)
	}
}

type OIDCVerifier struct {
	cfg      config.OIDC
	verifier atomic.Pointer[oidc.IDTokenVerifier]
}

func NewOIDCVerifier(cfg config.OIDC) *OIDCVerifier {
	return &OIDCVerifier{cfg: cfg}
}

func (v *OIDCVerifier) Start(ctx context.Context) error {
	backoff := time.Second
	for {
		provider, err := oidc.NewProvider(ctx, v.cfg.Issuer)
		if err == nil {
			v.verifier.Store(provider.Verifier(&oidc.Config{ClientID: v.cfg.Audience}))
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("%w: oidc discovery at %s: %v", application.ErrUnavailable, v.cfg.Issuer, err)
		case <-time.After(backoff):
			if backoff < 8*time.Second {
				backoff *= 2
			}
		}
	}
}

type rawClaims struct {
	Subject     string `json:"sub"`
	ClientID    string `json:"azp"`
	ProviderID  string `json:"providerId"`
	RealmAccess struct {
		Roles []string `json:"roles"`
	} `json:"realm_access"`
}

func (v *OIDCVerifier) Verify(ctx context.Context, rawToken string) (Claims, error) {
	verifier := v.verifier.Load()
	if verifier == nil {
		return Claims{}, ErrAuthNotReady
	}
	token, err := verifier.Verify(ctx, rawToken)
	if err != nil {
		return Claims{}, fmt.Errorf("%w: %v", ErrUnauthenticated, err)
	}
	var raw rawClaims
	if err := token.Claims(&raw); err != nil {
		return Claims{}, fmt.Errorf("%w: unreadable claims: %v", ErrUnauthenticated, err)
	}
	return Claims{
		Subject:    raw.Subject,
		ClientID:   raw.ClientID,
		ProviderID: raw.ProviderID,
		Roles:      raw.RealmAccess.Roles,
	}, nil
}

func Authenticate(verifier TokenVerifier) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			raw, ok := bearerToken(r.Header.Get("Authorization"))
			if !ok {
				writeError(w, r, fmt.Errorf("%w: missing bearer token", ErrUnauthenticated))
				return
			}
			claims, err := verifier.Verify(r.Context(), raw)
			if err != nil {
				writeError(w, r, err)
				return
			}
			principal, err := PrincipalFromClaims(claims)
			if err != nil {
				writeError(w, r, err)
				return
			}
			next.ServeHTTP(w, r.WithContext(application.WithPrincipal(r.Context(), principal)))
		})
	}
}

func bearerToken(header string) (string, bool) {
	const prefix = "bearer "
	if len(header) <= len(prefix) || !strings.EqualFold(header[:len(prefix)], prefix) {
		return "", false
	}
	token := strings.TrimSpace(header[len(prefix):])
	return token, token != ""
}

func principalFrom(r *http.Request) (application.Principal, error) {
	p, ok := application.PrincipalFrom(r.Context())
	if !ok {
		return application.Principal{}, fmt.Errorf("%w: no principal in context", ErrUnauthenticated)
	}
	return p, nil
}
