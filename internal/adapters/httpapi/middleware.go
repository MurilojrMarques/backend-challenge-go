package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"runtime/debug"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"

	"github.com/MurilojrMarques/backend-challenge-go/internal/application"
)

const CorrelationHeader = "X-Correlation-Id"

type correlationKey struct{}

func correlationFrom(ctx context.Context) string {
	id, _ := ctx.Value(correlationKey{}).(string)
	return id
}

func correlation(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(CorrelationHeader)
		if !application.ValidCorrelationID(id) {
			id = uuid.Must(uuid.NewV7()).String()
		}
		w.Header().Set(CorrelationHeader, id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), correlationKey{}, id)))
	})
}

type logFields struct {
	mu    sync.Mutex
	attrs []any
}

type logFieldsKey struct{}

func annotate(ctx context.Context, attrs ...any) {
	f, ok := ctx.Value(logFieldsKey{}).(*logFields)
	if !ok {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.attrs = append(f.attrs, attrs...)
}

func (f *logFields) snapshot() []any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]any(nil), f.attrs...)
}

func requestTelemetry(base *slog.Logger, observer RequestObserver) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			l := base.With("correlationId", correlationFrom(r.Context()))
			ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
			fields := &logFields{}
			ctx := context.WithValue(withLogger(r.Context(), l), logFieldsKey{}, fields)

			next.ServeHTTP(ww, r.WithContext(ctx))

			elapsed := time.Since(start)
			route := chi.RouteContext(r.Context()).RoutePattern()
			status := ww.Status()
			if status == 0 {
				status = http.StatusOK
			}
			if observer != nil {
				observer.ObserveRequest(r.Method, route, status, elapsed)
			}

			attrs := []any{
				"method", r.Method,
				"route", route,
				"status", status,
				"bytes", ww.BytesWritten(),
				"durationMs", elapsed.Milliseconds(),
			}
			attrs = append(attrs, fields.snapshot()...)
			switch {
			case status >= 500:
				l.ErrorContext(r.Context(), "http request", attrs...)
			case status >= 400:
				l.WarnContext(r.Context(), "http request", attrs...)
			default:
				l.InfoContext(r.Context(), "http request", attrs...)
			}
		})
	}
}

func requestTimeout(d time.Duration) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx, cancel := context.WithTimeout(r.Context(), d)
			defer cancel()
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				if rec == http.ErrAbortHandler {
					panic(rec)
				}
				logger(r.Context()).ErrorContext(r.Context(), "panic recovered", "panic", rec, "stack", string(debug.Stack()))
				writeJSON(w, http.StatusInternalServerError, ErrorBody{Code: "INTERNAL_ERROR", Message: "unexpected error", CorrelationID: correlationFrom(r.Context())})
			}
		}()
		next.ServeHTTP(w, r)
	})
}
