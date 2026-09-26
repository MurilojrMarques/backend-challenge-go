//go:build integration

package integration

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/fx"

	"github.com/MurilojrMarques/backend-challenge-go/internal/adapters/httpapi"
	"github.com/MurilojrMarques/backend-challenge-go/internal/bootstrap"
	"github.com/MurilojrMarques/backend-challenge-go/test/testutil"
)

func TestFxApplicationStartsServesAndStops(t *testing.T) {
	for name, value := range map[string]string{
		"HTTP_ADDR":                       "127.0.0.1:0",
		"DATABASE_URL":                    pg.AppURL(),
		"OIDC_ISSUER":                     kc.Issuer(),
		"OIDC_AUDIENCE":                   testutil.Audience,
		"AWS_REGION":                      testutil.Region,
		"AWS_ENDPOINT_URL":                ls.URL,
		"SQS_WAGER_QUEUE_URL":             ls.Queue(testutil.WagerQueue),
		"SQS_WAGER_DLQ_URL":               ls.Queue(testutil.WagerDLQ),
		"SQS_EVENTS_QUEUE_URL":            ls.Queue(testutil.EventsQueue),
		"SQS_WAIT_TIME_SECONDS":           "1",
		"OUTBOX_POLL_INTERVAL":            "200ms",
		"PENDING_REFERENCE_POLL_INTERVAL": "200ms",
	} {
		t.Setenv(name, value)
	}

	var server *httpapi.Server
	app := fx.New(bootstrap.New(bootstrap.AllRoles()), fx.NopLogger, fx.Populate(&server))
	require.NoError(t, app.Err(), "the graph resolves with every role enabled")

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	require.NoError(t, app.Start(ctx), "pool ping, OIDC discovery, HTTP listener and workers all come up")
	base := "http://" + server.Addr().String()

	res, err := testutil.NewClient(base, "").Get(ctx, "/health/ready")
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, res.Status, string(res.Raw))
	checks, _ := res.Body["checks"].(map[string]any)
	assert.Equal(t, "up", checks["postgres"])
	assert.Equal(t, "up", checks["sqs"])

	res, err = testutil.NewClient(base, fetchToken(t, testutil.InternalClient)).OpenWallet(ctx, newID().String(), "10.00")
	require.NoError(t, err)
	assert.Equal(t, http.StatusCreated, res.Status, "a real token goes through the fully wired application")

	stopCtx, stopCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer stopCancel()
	require.NoError(t, app.Stop(stopCtx), "workers, server and pool stop within the deadline")

	_, err = testutil.NewClient(base, "").Get(ctx, "/health/live")
	assert.Error(t, err, "the listener is closed after stop")
}
