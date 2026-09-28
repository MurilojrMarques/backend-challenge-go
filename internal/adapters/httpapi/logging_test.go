package httpapi_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/MurilojrMarques/backend-challenge-go/internal/adapters/httpapi"
	"github.com/MurilojrMarques/backend-challenge-go/internal/application/apptest"
)

type logBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *logBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *logBuffer) requestLines(t *testing.T, route string) []map[string]any {
	t.Helper()
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(b.buf.String()), "\n") {
		if line == "" {
			continue
		}
		var entry map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &entry))
		if entry["msg"] == "http request" && entry["route"] == route {
			out = append(out, entry)
		}
	}
	return out
}

func loggedAPI(t *testing.T) (*api, *logBuffer) {
	t.Helper()
	h := apptest.NewHarness(t)
	logs := &logBuffer{}
	handler, err := httpapi.NewRouter(httpapi.Deps{
		Options: httpapi.Options{EnableAPI: true},
		Logger:  slog.New(slog.NewJSONHandler(logs, nil)),
		Verifier: fakeVerifier{tokens: map[string]httpapi.Claims{
			tokenInternal:  {Subject: "svc", Roles: []string{httpapi.RoleInternal}},
			tokenProviderA: {Subject: "a", ProviderID: "provider-a", Roles: []string{httpapi.RoleProvider}},
		}},
		Wallets: h.Wallets,
		Wagers:  h.Wagers,
	})
	require.NoError(t, err)
	return &api{h: h, handler: handler}, logs
}

func TestRequestLogCarriesOperationIdentifiers(t *testing.T) {
	t.Parallel()
	a, logs := loggedAPI(t)
	w := a.h.OpenWallet(t, "100.00")

	headers := idem("bet-log")
	headers[httpapi.CorrelationHeader] = "corr-log-1"
	res := a.do(t, http.MethodPost, "/wagering/transactions", tokenProviderA, wagerBody(w.ID.String(), w.PlayerID.String(), "BET", "bet-log", "10.00"), headers)
	require.Equal(t, http.StatusOK, res.rec.Code, res.rec.Body.String())
	txID, _ := res.body["transactionId"].(string)

	lines := logs.requestLines(t, "/wagering/transactions")
	require.Len(t, lines, 1)
	entry := lines[0]
	assert.Equal(t, "corr-log-1", entry["correlationId"])
	assert.Equal(t, "provider-a", entry["providerId"])
	assert.Equal(t, "a", entry["subject"])
	assert.Equal(t, "provider", entry["role"])
	assert.Equal(t, w.ID.String(), entry["walletId"])
	assert.Equal(t, "bet-log", entry["externalTransactionId"])
	assert.Equal(t, txID, entry["transactionId"])
	assert.Equal(t, "PROCESSED", entry["wagerStatus"])
	assert.Equal(t, false, entry["idempotentReplay"])

	res = a.do(t, http.MethodGet, "/wagering/transactions/"+txID, tokenProviderA, nil, nil)
	require.Equal(t, http.StatusOK, res.rec.Code)
	byID := logs.requestLines(t, "/wagering/transactions/{transactionId}")
	require.Len(t, byID, 1)
	assert.Equal(t, txID, byID[0]["transactionId"])
	assert.Equal(t, w.ID.String(), byID[0]["walletId"])

	res = a.do(t, http.MethodGet, "/wallets/"+w.ID.String(), tokenInternal, nil, nil)
	require.Equal(t, http.StatusOK, res.rec.Code)
	walletLines := logs.requestLines(t, "/wallets/{walletId}")
	require.Len(t, walletLines, 1)
	assert.Equal(t, w.ID.String(), walletLines[0]["walletId"])
	assert.Equal(t, "internal", walletLines[0]["role"])
	assert.NotContains(t, walletLines[0], "providerId")
}

func TestRequestLogNeverCarriesCredentialsOrAmounts(t *testing.T) {
	t.Parallel()
	a, logs := loggedAPI(t)
	w := a.h.OpenWallet(t, "100.00")

	res := a.do(t, http.MethodPost, "/wagering/transactions", tokenProviderA, wagerBody(w.ID.String(), w.PlayerID.String(), "BET", "bet-secret", "12.34"), idem("bet-secret"))
	require.Equal(t, http.StatusOK, res.rec.Code)

	logs.mu.Lock()
	raw := logs.buf.String()
	logs.mu.Unlock()
	assert.NotContains(t, raw, tokenProviderA, "tokens are never logged")
	assert.NotContains(t, raw, "12.34", "amounts are not logged")
	assert.NotContains(t, raw, w.PlayerID.String(), "player ids are not logged")
}
