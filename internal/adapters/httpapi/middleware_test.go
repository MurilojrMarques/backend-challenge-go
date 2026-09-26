package httpapi_test

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/MurilojrMarques/backend-challenge-go/internal/adapters/httpapi"
	"github.com/MurilojrMarques/backend-challenge-go/internal/domain/wager"
)

func TestRouterAnswersHeadAndAdvertisesAllowedMethods(t *testing.T) {
	t.Parallel()
	handler, err := httpapi.NewRouter(httpapi.Deps{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	require.NoError(t, err)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodHead, "/health/live", nil))
	assert.Equal(t, http.StatusOK, rec.Code, "HEAD is answered by the GET handler; net/http drops the body on the wire")
	assert.Equal(t, "application/json; charset=utf-8", rec.Header().Get("Content-Type"))

	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/health/live", nil))
	assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
	assert.Equal(t, "GET, HEAD", rec.Header().Get("Allow"))
	assert.Contains(t, rec.Body.String(), "METHOD_NOT_ALLOWED")
}

func TestUnauthenticatedResponsesCarryAChallenge(t *testing.T) {
	t.Parallel()
	a := newAPI(t)
	path := "/wallets/" + uuid.Must(uuid.NewV7()).String()

	res := a.do(t, http.MethodGet, path, "", nil, nil)
	assert.Equal(t, http.StatusUnauthorized, res.rec.Code)
	assert.Equal(t, `Bearer realm="wallet"`, res.rec.Header().Get("WWW-Authenticate"))

	res = a.do(t, http.MethodGet, path, "not-a-token", nil, nil)
	assert.Equal(t, http.StatusUnauthorized, res.rec.Code)
	assert.Equal(t, `Bearer realm="wallet", error="invalid_token"`, res.rec.Header().Get("WWW-Authenticate"))
}

func TestWrongContentTypeIsUnsupportedMediaType(t *testing.T) {
	t.Parallel()
	a := newAPI(t)
	body := `{"playerId":"` + uuid.Must(uuid.NewV7()).String() + `","initialBalance":{"amount":"1.00","currency":"BRL"}}`
	res := a.do(t, http.MethodPost, "/wallets", tokenInternal, body, map[string]string{"Content-Type": "text/plain"})
	assert.Equal(t, http.StatusUnsupportedMediaType, res.rec.Code)
	assert.Equal(t, "UNSUPPORTED_MEDIA_TYPE", res.body["code"])
}

func TestExternalTransactionIdPathSegmentIsDecoded(t *testing.T) {
	t.Parallel()
	a := newAPI(t)
	w := a.h.OpenWallet(t, "100.00")
	submitted := a.h.Submit(t, a.h.Command(w, "BET", "round/7-bet", "10.00"))
	require.Equal(t, wager.Processed, submitted.Status)

	res := a.do(t, http.MethodGet, "/providers/provider-a/wagering/transactions/round%2F7-bet", tokenProviderA, nil, nil)
	assert.Equal(t, http.StatusOK, res.rec.Code)
	assert.Equal(t, submitted.TransactionID.String(), res.body["transactionId"])

	res = a.do(t, http.MethodGet, "/providers/provider-a/wagering/transactions/bet%00", tokenProviderA, nil, nil)
	assert.Equal(t, http.StatusBadRequest, res.rec.Code, "control characters never reach the database")
	assert.Equal(t, "INVALID_INPUT", res.body["code"])
}

type observed struct {
	method string
	route  string
	status int
}

type recordingObserver struct {
	mu      sync.Mutex
	entries []observed
}

func (o *recordingObserver) ObserveRequest(method, route string, status int, _ time.Duration) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.entries = append(o.entries, observed{method: method, route: route, status: status})
}

func TestRouterReportsRequestsToObserver(t *testing.T) {
	t.Parallel()
	obs := &recordingObserver{}
	handler, err := httpapi.NewRouter(httpapi.Deps{
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		Observer: obs,
	})
	require.NoError(t, err)

	for _, path := range []string{"/health/live", "/nope"} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	}

	require.Len(t, obs.entries, 2)
	assert.Equal(t, observed{method: http.MethodGet, route: "/health/live", status: http.StatusOK}, obs.entries[0])
	assert.Equal(t, http.StatusNotFound, obs.entries[1].status)
}
