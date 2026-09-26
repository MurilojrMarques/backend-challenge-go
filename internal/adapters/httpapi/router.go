package httpapi

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/MurilojrMarques/backend-challenge-go/internal/application"
	"github.com/MurilojrMarques/backend-challenge-go/internal/application/wagering"
	"github.com/MurilojrMarques/backend-challenge-go/internal/application/wallets"
)

const (
	defaultMaxBodyBytes   = 64 * 1024
	defaultRequestTimeout = 10 * time.Second
)

type Options struct {
	EnableAPI      bool
	MaxBodyBytes   int64
	RequestTimeout time.Duration
}

func (o Options) withDefaults() Options {
	if o.MaxBodyBytes <= 0 {
		o.MaxBodyBytes = defaultMaxBodyBytes
	}
	if o.RequestTimeout <= 0 {
		o.RequestTimeout = defaultRequestTimeout
	}
	return o
}

type RequestObserver interface {
	ObserveRequest(method, route string, status int, elapsed time.Duration)
}

type Deps struct {
	Options  Options
	Logger   *slog.Logger
	Verifier TokenVerifier
	Wallets  *wallets.Service
	Wagers   *wagering.Service
	Health   []application.HealthChecker
	Metrics  http.Handler
	Observer RequestObserver
}

func (d Deps) validate() error {
	if d.Logger == nil {
		return errors.New("httpapi: logger is required")
	}
	if !d.Options.EnableAPI {
		return nil
	}
	if d.Verifier == nil || d.Wallets == nil || d.Wagers == nil {
		return errors.New("httpapi: verifier, wallets and wagering services are required when the API is enabled")
	}
	return nil
}

func allowedMethods(mux chi.Router, path string) []string {
	var out []string
	for _, m := range []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodOptions} {
		if mux.Match(chi.NewRouteContext(), m, path) {
			out = append(out, m)
			if m == http.MethodGet {
				out = append(out, http.MethodHead)
			}
		}
	}
	return out
}

func NewRouter(d Deps) (http.Handler, error) {
	if err := d.validate(); err != nil {
		return nil, err
	}
	opts := d.Options.withDefaults()

	r := chi.NewRouter()
	r.Use(correlation, requestTelemetry(d.Logger, d.Observer), recoverer, requestTimeout(opts.RequestTimeout), middleware.GetHead)

	r.NotFound(func(w http.ResponseWriter, r *http.Request) {
		writeError(w, r, application.ErrNotFound)
	})
	r.MethodNotAllowed(func(w http.ResponseWriter, req *http.Request) {
		if allowed := allowedMethods(r, req.URL.Path); len(allowed) > 0 {
			w.Header().Set("Allow", strings.Join(allowed, ", "))
		}
		writeJSON(w, http.StatusMethodNotAllowed, ErrorBody{Code: "METHOD_NOT_ALLOWED", Message: "method not allowed", CorrelationID: correlationFrom(req.Context())})
	})

	health := &healthHandler{checkers: d.Health}
	r.Get("/health/live", health.live)
	r.Get("/health/ready", health.ready)
	if d.Metrics != nil {
		r.Method(http.MethodGet, "/metrics", d.Metrics)
	}

	if !opts.EnableAPI {
		return r, nil
	}

	walletH := &walletHandler{service: d.Wallets, maxBodyBytes: opts.MaxBodyBytes}
	wagerH := &wagerHandler{service: d.Wagers, maxBodyBytes: opts.MaxBodyBytes}

	r.Group(func(r chi.Router) {
		r.Use(Authenticate(d.Verifier))

		r.Post("/wallets", walletH.open)
		r.Get("/wallets/{walletId}", walletH.get)
		r.Get("/wallets/{walletId}/ledger", walletH.ledger)
		r.Post("/wallets/{walletId}/reconciliation", walletH.reconcile)

		r.Post("/wagering/transactions", wagerH.submit)
		r.Get("/wagering/transactions/{transactionId}", wagerH.getByID)
		r.Get("/providers/{providerId}/wagering/transactions/{externalTransactionId}", wagerH.getByExternalID)
	})

	return r, nil
}
