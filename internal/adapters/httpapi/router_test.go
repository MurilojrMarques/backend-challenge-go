package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/MurilojrMarques/backend-challenge-go/internal/adapters/httpapi"
	"github.com/MurilojrMarques/backend-challenge-go/internal/application"
	"github.com/MurilojrMarques/backend-challenge-go/internal/application/apptest"
)

type fakeVerifier struct {
	tokens map[string]httpapi.Claims
}

func (f fakeVerifier) Verify(_ context.Context, raw string) (httpapi.Claims, error) {
	c, ok := f.tokens[raw]
	if !ok {
		return httpapi.Claims{}, httpapi.ErrUnauthenticated
	}
	return c, nil
}

type fakeChecker struct {
	name string
	err  error
}

func (c fakeChecker) Name() string                { return c.name }
func (c fakeChecker) Check(context.Context) error { return c.err }

type api struct {
	h       *apptest.Harness
	handler http.Handler
}

const (
	tokenInternal  = "tok-internal"
	tokenProviderA = "tok-provider-a"
	tokenProviderB = "tok-provider-b"
	tokenNoRole    = "tok-no-role"
	tokenNoID      = "tok-provider-without-id"
)

func newAPI(t *testing.T, checkers ...application.HealthChecker) *api {
	t.Helper()
	h := apptest.NewHarness(t)
	verifier := fakeVerifier{tokens: map[string]httpapi.Claims{
		tokenInternal:  {Subject: "svc", Roles: []string{httpapi.RoleInternal}},
		tokenProviderA: {Subject: "a", ProviderID: "provider-a", Roles: []string{httpapi.RoleProvider}},
		tokenProviderB: {Subject: "b", ProviderID: "provider-b", Roles: []string{httpapi.RoleProvider}},
		tokenNoRole:    {Subject: "nobody"},
		tokenNoID:      {Subject: "x", Roles: []string{httpapi.RoleProvider}},
	}}
	handler, err := httpapi.NewRouter(httpapi.Deps{
		Options:  httpapi.Options{EnableAPI: true, MaxBodyBytes: 2048},
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		Verifier: verifier,
		Wallets:  h.Wallets,
		Wagers:   h.Wagers,
		Health:   checkers,
	})
	require.NoError(t, err)
	return &api{h: h, handler: handler}
}

type response struct {
	rec  *httptest.ResponseRecorder
	body map[string]any
}

func (a *api) do(t *testing.T, method, path, token string, body any, headers map[string]string) response {
	t.Helper()
	var reader io.Reader
	switch b := body.(type) {
	case nil:
	case string:
		reader = strings.NewReader(b)
	default:
		raw, err := json.Marshal(b)
		require.NoError(t, err)
		reader = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, path, reader)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	a.handler.ServeHTTP(rec, req)

	out := response{rec: rec}
	if rec.Body.Len() > 0 {
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out.body), "body: %s", rec.Body.String())
	}
	return out
}

func (a *api) openWallet(t *testing.T, amount string) (string, string) {
	t.Helper()
	res := a.do(t, http.MethodPost, "/wallets", tokenInternal, map[string]any{
		"playerId":       uuid.Must(uuid.NewV7()).String(),
		"initialBalance": map[string]string{"amount": amount, "currency": "BRL"},
	}, nil)
	require.Equal(t, http.StatusCreated, res.rec.Code, res.rec.Body.String())
	return res.body["id"].(string), res.body["playerId"].(string)
}

func wagerBody(walletID, playerID, kind, externalID, amount string) map[string]any {
	return map[string]any{
		"providerId":            "provider-a",
		"externalTransactionId": externalID,
		"playerId":              playerID,
		"walletId":              walletID,
		"roundId":               "round-1",
		"gameId":                "fortune-chimp",
		"kind":                  kind,
		"money":                 map[string]string{"amount": amount, "currency": "BRL"},
	}
}

func idem(externalID string) map[string]string {
	return map[string]string{httpapi.IdempotencyKeyHeader: "provider-a:" + externalID}
}

func TestHealth(t *testing.T) {
	t.Parallel()
	a := newAPI(t, fakeChecker{name: "postgres"}, fakeChecker{name: "sqs"})

	res := a.do(t, http.MethodGet, "/health/live", "", nil, nil)
	assert.Equal(t, http.StatusOK, res.rec.Code)
	assert.Equal(t, "ok", res.body["status"])

	res = a.do(t, http.MethodGet, "/health/ready", "", nil, nil)
	assert.Equal(t, http.StatusOK, res.rec.Code)
	assert.Equal(t, map[string]any{"postgres": "up", "sqs": "up"}, res.body["checks"])

	degraded := newAPI(t, fakeChecker{name: "postgres"}, fakeChecker{name: "sqs", err: errors.New("down")})
	res = degraded.do(t, http.MethodGet, "/health/ready", "", nil, nil)
	assert.Equal(t, http.StatusServiceUnavailable, res.rec.Code)
	assert.Equal(t, "degraded", res.body["status"])
	assert.Equal(t, "down", res.body["checks"].(map[string]any)["sqs"])
}

func TestAuthentication(t *testing.T) {
	t.Parallel()
	a := newAPI(t)
	path := "/wallets/" + uuid.Must(uuid.NewV7()).String()

	cases := map[string]struct {
		auth string
		code int
		body string
	}{
		"missing":        {"", http.StatusUnauthorized, "UNAUTHENTICATED"},
		"invalid token":  {"Bearer nope", http.StatusUnauthorized, "UNAUTHENTICATED"},
		"basic scheme":   {"Basic abc", http.StatusUnauthorized, "UNAUTHENTICATED"},
		"empty bearer":   {"Bearer ", http.StatusUnauthorized, "UNAUTHENTICATED"},
		"no role":        {"Bearer " + tokenNoRole, http.StatusForbidden, "FORBIDDEN"},
		"provider no id": {"Bearer " + tokenNoID, http.StatusForbidden, "FORBIDDEN"},
		"wrong role":     {"Bearer " + tokenProviderA, http.StatusForbidden, "FORBIDDEN"},
	}
	for name, tc := range cases {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		if tc.auth != "" {
			req.Header.Set("Authorization", tc.auth)
		}
		rec := httptest.NewRecorder()
		a.handler.ServeHTTP(rec, req)
		assert.Equal(t, tc.code, rec.Code, name)
		assert.Contains(t, rec.Body.String(), tc.body, name)
	}

	res := a.do(t, http.MethodGet, path, tokenInternal, nil, nil)
	assert.Equal(t, http.StatusNotFound, res.rec.Code, "authenticated internal reaches the handler")
}

func TestOpenWallet(t *testing.T) {
	t.Parallel()
	a := newAPI(t)
	playerID := uuid.Must(uuid.NewV7()).String()
	body := map[string]any{"playerId": playerID, "initialBalance": map[string]string{"amount": "1000.00", "currency": "BRL"}}

	res := a.do(t, http.MethodPost, "/wallets", tokenInternal, body, nil)
	require.Equal(t, http.StatusCreated, res.rec.Code, res.rec.Body.String())
	assert.Equal(t, playerID, res.body["playerId"])
	assert.Equal(t, map[string]any{"amount": "1000.00", "currency": "BRL"}, res.body["balance"])
	assert.Equal(t, float64(1), res.body["version"])
	assert.Equal(t, "/wallets/"+res.body["id"].(string), res.rec.Header().Get("Location"))

	res = a.do(t, http.MethodPost, "/wallets", tokenInternal, body, nil)
	assert.Equal(t, http.StatusConflict, res.rec.Code)
	assert.Equal(t, "WALLET_ALREADY_EXISTS", res.body["code"])

	res = a.do(t, http.MethodPost, "/wallets", tokenProviderA, body, nil)
	assert.Equal(t, http.StatusForbidden, res.rec.Code)

	invalid := map[string]struct {
		body any
		code string
	}{
		"bad money":     {map[string]any{"playerId": playerID, "initialBalance": map[string]string{"amount": "10", "currency": "BRL"}}, "INVALID_MONEY"},
		"scale":         {map[string]any{"playerId": playerID, "initialBalance": map[string]string{"amount": "10.000", "currency": "BRL"}}, "INVALID_MONEY_SCALE"},
		"negative":      {map[string]any{"playerId": playerID, "initialBalance": map[string]string{"amount": "-1.00", "currency": "BRL"}}, "INVALID_MONEY"},
		"bad player":    {map[string]any{"playerId": "x", "initialBalance": map[string]string{"amount": "1.00", "currency": "BRL"}}, "INVALID_INPUT"},
		"unknown field": {map[string]any{"playerId": playerID, "initialBalance": map[string]string{"amount": "1.00", "currency": "BRL"}, "extra": 1}, "MALFORMED_BODY"},
		"not json":      {"{", "MALFORMED_BODY"},
		"trailing data": {`{"playerId":"` + playerID + `","initialBalance":{"amount":"1.00","currency":"BRL"}} {}`, "MALFORMED_BODY"},
		"number amount": {`{"playerId":"` + playerID + `","initialBalance":{"amount":10.00,"currency":"BRL"}}`, "MALFORMED_BODY"},
	}
	for name, tc := range invalid {
		res := a.do(t, http.MethodPost, "/wallets", tokenInternal, tc.body, nil)
		assert.Equal(t, http.StatusBadRequest, res.rec.Code, name)
		assert.Equal(t, tc.code, res.body["code"], name)
	}

	huge := `{"playerId":"` + playerID + `","initialBalance":{"amount":"1.00","currency":"BRL"},"pad":"` + strings.Repeat("x", 4096) + `"}`
	res = a.do(t, http.MethodPost, "/wallets", tokenInternal, huge, nil)
	assert.Equal(t, http.StatusRequestEntityTooLarge, res.rec.Code)
}

func TestGetWallet(t *testing.T) {
	t.Parallel()
	a := newAPI(t)
	walletID, _ := a.openWallet(t, "5.00")

	res := a.do(t, http.MethodGet, "/wallets/"+walletID, tokenInternal, nil, nil)
	assert.Equal(t, http.StatusOK, res.rec.Code)
	assert.Equal(t, walletID, res.body["id"])

	res = a.do(t, http.MethodGet, "/wallets/not-a-uuid", tokenInternal, nil, nil)
	assert.Equal(t, http.StatusBadRequest, res.rec.Code)
	res = a.do(t, http.MethodGet, "/wallets/"+uuid.Must(uuid.NewV7()).String(), tokenInternal, nil, nil)
	assert.Equal(t, http.StatusNotFound, res.rec.Code)
	assert.Equal(t, "NOT_FOUND", res.body["code"])
}

func TestSubmitWager(t *testing.T) {
	t.Parallel()
	a := newAPI(t)
	walletID, playerID := a.openWallet(t, "100.00")

	res := a.do(t, http.MethodPost, "/wagering/transactions", tokenProviderA, wagerBody(walletID, playerID, "BET", "b1", "25.00"), nil)
	assert.Equal(t, http.StatusBadRequest, res.rec.Code)
	assert.Equal(t, "MISSING_IDEMPOTENCY_KEY", res.body["code"])

	res = a.do(t, http.MethodPost, "/wagering/transactions", tokenProviderA, wagerBody(walletID, playerID, "BET", "b1", "25.00"), idem("b1"))
	require.Equal(t, http.StatusOK, res.rec.Code, res.rec.Body.String())
	assert.Equal(t, "PROCESSED", res.body["status"])
	assert.Equal(t, map[string]any{"amount": "75.00", "currency": "BRL"}, res.body["balance"])
	assert.Equal(t, false, res.body["idempotentReplay"])
	assert.NotContains(t, res.body, "failureCode")
	transactionID := res.body["transactionId"].(string)

	res = a.do(t, http.MethodPost, "/wagering/transactions", tokenProviderA, wagerBody(walletID, playerID, "BET", "b1", "25.00"), idem("b1"))
	assert.Equal(t, http.StatusOK, res.rec.Code)
	assert.Equal(t, true, res.body["idempotentReplay"])
	assert.Equal(t, transactionID, res.body["transactionId"])

	res = a.do(t, http.MethodPost, "/wagering/transactions", tokenProviderA, wagerBody(walletID, playerID, "BET", "b1", "26.00"), idem("b1"))
	assert.Equal(t, http.StatusConflict, res.rec.Code)
	assert.Equal(t, "IDEMPOTENCY_PAYLOAD_CONFLICT", res.body["code"])

	res = a.do(t, http.MethodPost, "/wagering/transactions", tokenProviderA, wagerBody(walletID, playerID, "BET", "b1", "25.00"), map[string]string{httpapi.IdempotencyKeyHeader: "other"})
	assert.Equal(t, http.StatusConflict, res.rec.Code)
	assert.Equal(t, "IDEMPOTENCY_KEY_MISMATCH", res.body["code"])

	res = a.do(t, http.MethodPost, "/wagering/transactions", tokenProviderA, wagerBody(walletID, playerID, "BET", "b2", "500.00"), idem("b2"))
	assert.Equal(t, http.StatusUnprocessableEntity, res.rec.Code)
	assert.Equal(t, "REJECTED", res.body["status"])
	assert.Equal(t, "INSUFFICIENT_FUNDS", res.body["failureCode"])
	assert.Equal(t, false, res.body["correctable"])
	assert.Equal(t, map[string]any{"amount": "75.00", "currency": "BRL"}, res.body["balance"])

	refund := wagerBody(walletID, playerID, "REFUND", "r1", "10.00")
	refund["referenceExternalTransactionId"] = "missing-bet"
	res = a.do(t, http.MethodPost, "/wagering/transactions", tokenProviderA, refund, idem("r1"))
	assert.Equal(t, http.StatusAccepted, res.rec.Code)
	assert.Equal(t, "PENDING_REFERENCE", res.body["status"])
	assert.NotContains(t, res.body, "balance")

	res = a.do(t, http.MethodPost, "/wagering/transactions", tokenProviderB, wagerBody(walletID, playerID, "BET", "b3", "1.00"), idem("b3"))
	assert.Equal(t, http.StatusForbidden, res.rec.Code)
	res = a.do(t, http.MethodPost, "/wagering/transactions", tokenInternal, wagerBody(walletID, playerID, "BET", "b3", "1.00"), idem("b3"))
	assert.Equal(t, http.StatusForbidden, res.rec.Code)

	invalid := map[string]struct {
		mutate func(m map[string]any)
		code   string
	}{
		"opening kind":     {func(m map[string]any) { m["kind"] = "OPENING" }, "KIND_NOT_ALLOWED"},
		"unknown kind":     {func(m map[string]any) { m["kind"] = "DEPOSIT" }, "INVALID_INPUT"},
		"loss with amount": {func(m map[string]any) { m["kind"] = "LOSS" }, "INVALID_AMOUNT_FOR_KIND"},
		"bet with zero":    {func(m map[string]any) { m["money"] = map[string]string{"amount": "0.00", "currency": "BRL"} }, "INVALID_AMOUNT_FOR_KIND"},
		"scale":            {func(m map[string]any) { m["money"] = map[string]string{"amount": "1.000", "currency": "BRL"} }, "INVALID_MONEY_SCALE"},
		"bad currency":     {func(m map[string]any) { m["money"] = map[string]string{"amount": "1.00", "currency": "brl"} }, "INVALID_MONEY"},
		"refund no ref":    {func(m map[string]any) { m["kind"] = "REFUND" }, "INVALID_REFERENCE"},
		"bad wallet":       {func(m map[string]any) { m["walletId"] = "w" }, "INVALID_INPUT"},
	}
	for name, tc := range invalid {
		body := wagerBody(walletID, playerID, "BET", "x", "1.00")
		tc.mutate(body)
		res := a.do(t, http.MethodPost, "/wagering/transactions", tokenProviderA, body, idem("x"))
		assert.Equal(t, http.StatusBadRequest, res.rec.Code, name)
		assert.Equal(t, tc.code, res.body["code"], name)
	}

	res = a.do(t, http.MethodPost, "/wagering/transactions", tokenProviderA, wagerBody(uuid.Must(uuid.NewV7()).String(), playerID, "BET", "b9", "1.00"), idem("b9"))
	assert.Equal(t, http.StatusNotFound, res.rec.Code)
}

func TestTransactionQueries(t *testing.T) {
	t.Parallel()
	a := newAPI(t)
	walletID, playerID := a.openWallet(t, "100.00")
	res := a.do(t, http.MethodPost, "/wagering/transactions", tokenProviderA, wagerBody(walletID, playerID, "BET", "b1", "25.00"), idem("b1"))
	require.Equal(t, http.StatusOK, res.rec.Code)
	id := res.body["transactionId"].(string)

	res = a.do(t, http.MethodGet, "/wagering/transactions/"+id, tokenProviderA, nil, nil)
	assert.Equal(t, http.StatusOK, res.rec.Code)
	assert.Equal(t, "BET", res.body["kind"])
	assert.Equal(t, "provider-a", res.body["providerId"])
	assert.Equal(t, map[string]any{"amount": "75.00", "currency": "BRL"}, res.body["balance"])
	assert.NotEmpty(t, res.body["completedAt"])

	res = a.do(t, http.MethodGet, "/wagering/transactions/"+id, tokenProviderB, nil, nil)
	assert.Equal(t, http.StatusNotFound, res.rec.Code, "other provider cannot see it")
	res = a.do(t, http.MethodGet, "/wagering/transactions/"+id, tokenInternal, nil, nil)
	assert.Equal(t, http.StatusOK, res.rec.Code)

	res = a.do(t, http.MethodGet, "/providers/provider-a/wagering/transactions/b1", tokenProviderA, nil, nil)
	assert.Equal(t, http.StatusOK, res.rec.Code)
	assert.Equal(t, id, res.body["transactionId"])
	res = a.do(t, http.MethodGet, "/providers/provider-a/wagering/transactions/b1", tokenProviderB, nil, nil)
	assert.Equal(t, http.StatusNotFound, res.rec.Code)
	res = a.do(t, http.MethodGet, "/providers/provider-a/wagering/transactions/nope", tokenProviderA, nil, nil)
	assert.Equal(t, http.StatusNotFound, res.rec.Code)
}

func TestLedgerAndReconciliation(t *testing.T) {
	t.Parallel()
	a := newAPI(t)
	walletID, playerID := a.openWallet(t, "100.00")
	for _, id := range []string{"b1", "b2", "b3"} {
		res := a.do(t, http.MethodPost, "/wagering/transactions", tokenProviderA, wagerBody(walletID, playerID, "BET", id, "10.00"), idem(id))
		require.Equal(t, http.StatusOK, res.rec.Code)
	}

	res := a.do(t, http.MethodGet, "/wallets/"+walletID+"/ledger?limit=2", tokenInternal, nil, nil)
	require.Equal(t, http.StatusOK, res.rec.Code)
	entries := res.body["entries"].([]any)
	assert.Len(t, entries, 2)
	assert.Equal(t, "CREDIT", entries[0].(map[string]any)["direction"])
	cursor, _ := res.body["nextCursor"].(string)
	require.NotEmpty(t, cursor)

	res = a.do(t, http.MethodGet, "/wallets/"+walletID+"/ledger?limit=2&cursor="+cursor, tokenInternal, nil, nil)
	require.Equal(t, http.StatusOK, res.rec.Code)
	assert.Len(t, res.body["entries"].([]any), 2)
	assert.NotContains(t, res.body, "nextCursor")

	for _, q := range []string{"?cursor=!!!", "?cursor=bm9wZQ", "?limit=abc", "?limit=0", "?limit=999"} {
		res = a.do(t, http.MethodGet, "/wallets/"+walletID+"/ledger"+q, tokenInternal, nil, nil)
		assert.Equal(t, http.StatusBadRequest, res.rec.Code, q)
	}
	res = a.do(t, http.MethodGet, "/wallets/"+walletID+"/ledger", tokenProviderA, nil, nil)
	assert.Equal(t, http.StatusForbidden, res.rec.Code)

	res = a.do(t, http.MethodPost, "/wallets/"+walletID+"/reconciliation", tokenInternal, nil, nil)
	require.Equal(t, http.StatusOK, res.rec.Code)
	assert.Equal(t, true, res.body["consistent"])
	assert.Equal(t, map[string]any{"amount": "0.00", "currency": "BRL"}, res.body["difference"])
	assert.Equal(t, float64(4), res.body["checkedEntries"])
}

func TestCorrelationAndUnavailable(t *testing.T) {
	t.Parallel()
	a := newAPI(t)

	res := a.do(t, http.MethodGet, "/health/live", "", nil, map[string]string{httpapi.CorrelationHeader: "corr-123"})
	assert.Equal(t, "corr-123", res.rec.Header().Get(httpapi.CorrelationHeader))
	res = a.do(t, http.MethodGet, "/health/live", "", nil, nil)
	assert.NotEmpty(t, res.rec.Header().Get(httpapi.CorrelationHeader), "a correlation id is generated when absent")

	a.h.Store.FailNext(application.ErrUnavailable)
	res = a.do(t, http.MethodGet, "/wallets/"+uuid.Must(uuid.NewV7()).String(), tokenInternal, nil, map[string]string{httpapi.CorrelationHeader: "corr-503"})
	assert.Equal(t, http.StatusServiceUnavailable, res.rec.Code)
	assert.Equal(t, "TEMPORARILY_UNAVAILABLE", res.body["code"])
	assert.Equal(t, "1", res.rec.Header().Get("Retry-After"))
	assert.Equal(t, "corr-503", res.body["correlationId"])

	res = a.do(t, http.MethodGet, "/nope", "", nil, nil)
	assert.Equal(t, http.StatusNotFound, res.rec.Code)
	assert.Equal(t, "NOT_FOUND", res.body["code"])
	res = a.do(t, http.MethodDelete, "/health/live", "", nil, nil)
	assert.Equal(t, http.StatusMethodNotAllowed, res.rec.Code)
}

func TestAPIDisabledKeepsOnlyOpsRoutes(t *testing.T) {
	t.Parallel()
	h := apptest.NewHarness(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler, err := httpapi.NewRouter(httpapi.Deps{Options: httpapi.Options{EnableAPI: false}, Logger: logger})
	require.NoError(t, err)
	a := &api{h: h, handler: handler}

	res := a.do(t, http.MethodGet, "/health/ready", "", nil, nil)
	assert.Equal(t, http.StatusOK, res.rec.Code)
	res = a.do(t, http.MethodPost, "/wallets", tokenInternal, map[string]any{}, nil)
	assert.Equal(t, http.StatusNotFound, res.rec.Code)

	_, err = httpapi.NewRouter(httpapi.Deps{Options: httpapi.Options{EnableAPI: true}, Logger: logger, Wallets: h.Wallets})
	assert.Error(t, err, "API enabled without verifier and wagering service fails at construction")
	_, err = httpapi.NewRouter(httpapi.Deps{})
	assert.Error(t, err, "logger is mandatory")
}

func TestSecurityHeadersAndTimeout(t *testing.T) {
	t.Parallel()
	a := newAPI(t)

	res := a.do(t, http.MethodGet, "/health/live", "", nil, nil)
	assert.Equal(t, "nosniff", res.rec.Header().Get("X-Content-Type-Options"))
	assert.Equal(t, "no-store", res.rec.Header().Get("Cache-Control"))
	assert.Equal(t, "application/json; charset=utf-8", res.rec.Header().Get("Content-Type"))

	slow := fakeChecker{name: "slow", err: nil}
	handler, err := httpapi.NewRouter(httpapi.Deps{
		Options: httpapi.Options{RequestTimeout: 20 * time.Millisecond},
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		Health:  []application.HealthChecker{slowChecker{fakeChecker: slow}},
	})
	require.NoError(t, err)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health/ready", nil))
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code, "request deadline propagates to dependencies")
}

type slowChecker struct{ fakeChecker }

func (c slowChecker) Check(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(time.Second):
		return nil
	}
}

func TestPrincipalFromClaims(t *testing.T) {
	t.Parallel()

	p, err := httpapi.PrincipalFromClaims(httpapi.Claims{Subject: "s", Roles: []string{httpapi.RoleInternal, httpapi.RoleProvider}, ProviderID: "x"})
	require.NoError(t, err)
	assert.Equal(t, application.RoleInternal, p.Role, "internal wins when both roles are present")

	p, err = httpapi.PrincipalFromClaims(httpapi.Claims{ClientID: "provider-a", Roles: []string{httpapi.RoleProvider}, ProviderID: "provider-a"})
	require.NoError(t, err)
	assert.Equal(t, "provider-a", p.Subject, "azp is used when sub is absent")
	assert.Equal(t, application.RoleProvider, p.Role)

	_, err = httpapi.PrincipalFromClaims(httpapi.Claims{Subject: "s", Roles: []string{"admin"}})
	assert.ErrorIs(t, err, application.ErrForbidden)
	_, err = httpapi.PrincipalFromClaims(httpapi.Claims{Subject: "s", Roles: []string{httpapi.RoleProvider}})
	assert.ErrorIs(t, err, application.ErrForbidden)
}
