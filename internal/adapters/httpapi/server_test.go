package httpapi_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/MurilojrMarques/backend-challenge-go/internal/adapters/httpapi"
	"github.com/MurilojrMarques/backend-challenge-go/internal/config"
)

func TestServerLifecycle(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := config.HTTP{Addr: "127.0.0.1:0", ReadTimeout: time.Second, WriteTimeout: time.Second}
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })

	s := httpapi.NewServer(cfg, handler, logger)
	require.NoError(t, s.Start(context.Background()))
	t.Cleanup(func() { _ = s.Stop(context.Background()) })

	url := "http://" + s.Addr().String() + "/anything"
	resp, err := http.Get(url)
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusNoContent, resp.StatusCode)

	busy := httpapi.NewServer(config.HTTP{Addr: s.Addr().String()}, handler, logger)
	assert.Error(t, busy.Start(context.Background()), "a busy port fails at start, not at first request")

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	require.NoError(t, s.Stop(ctx))

	_, err = http.Get(url)
	assert.Error(t, err, "server no longer accepts connections after Stop")
}
