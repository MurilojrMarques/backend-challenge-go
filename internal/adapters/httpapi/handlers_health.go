package httpapi

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/MurilojrMarques/backend-challenge-go/internal/application"
)

const readinessTimeout = 2 * time.Second

type healthHandler struct {
	checkers []application.HealthChecker
}

type healthResponse struct {
	Status string            `json:"status"`
	Checks map[string]string `json:"checks,omitempty"`
}

func (h *healthHandler) live(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, healthResponse{Status: "ok"})
}

func (h *healthHandler) ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), readinessTimeout)
	defer cancel()

	var (
		mu     sync.Mutex
		wg     sync.WaitGroup
		checks = make(map[string]string, len(h.checkers))
		ok     = true
	)
	for _, c := range h.checkers {
		wg.Add(1)
		go func(c application.HealthChecker) {
			defer wg.Done()
			err := c.Check(ctx)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				checks[c.Name()] = "down"
				ok = false
				return
			}
			checks[c.Name()] = "up"
		}(c)
	}
	wg.Wait()

	status, body := http.StatusOK, healthResponse{Status: "ok", Checks: checks}
	if !ok {
		status, body.Status = http.StatusServiceUnavailable, "degraded"
	}
	writeJSON(w, status, body)
}
