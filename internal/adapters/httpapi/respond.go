package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/MurilojrMarques/backend-challenge-go/internal/application"
	"github.com/MurilojrMarques/backend-challenge-go/internal/domain/money"
	"github.com/MurilojrMarques/backend-challenge-go/internal/domain/wager"
)

type ErrorBody struct {
	Code          string `json:"code"`
	Message       string `json:"message"`
	CorrelationID string `json:"correlationId,omitempty"`
}

type apiError struct {
	status int
	code   string
	msg    string
}

var (
	errBodyTooLarge = errors.New("httpapi: request body too large")
	errBadJSON      = errors.New("httpapi: malformed json body")
)

func decodeJSON(w http.ResponseWriter, r *http.Request, maxBytes int64, dst any) error {
	if ct := r.Header.Get("Content-Type"); ct != "" && !strings.HasPrefix(strings.ToLower(ct), "application/json") {
		return fmt.Errorf("%w: content-type must be application/json", errBadJSON)
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return errBodyTooLarge
		}
		return fmt.Errorf("%w: %v", errBadJSON, err)
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		return fmt.Errorf("%w: unexpected data after json object", errBadJSON)
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	if v == nil {
		return
	}
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, r *http.Request, err error) {
	e := classify(err)
	if e.status == http.StatusServiceUnavailable {
		w.Header().Set("Retry-After", "1")
	}
	if e.status >= 500 {
		logger(r.Context()).ErrorContext(r.Context(), "request failed", "code", e.code, "err", err)
	}
	writeJSON(w, e.status, ErrorBody{Code: e.code, Message: e.msg, CorrelationID: correlationFrom(r.Context())})
}

func classify(err error) apiError {
	var conflict *application.ConflictError
	switch {
	case errors.Is(err, ErrUnauthenticated):
		return apiError{http.StatusUnauthorized, "UNAUTHENTICATED", "missing, invalid or expired credentials"}
	case errors.Is(err, application.ErrForbidden):
		return apiError{http.StatusForbidden, "FORBIDDEN", "the authenticated identity cannot perform this operation"}
	case errors.Is(err, errBodyTooLarge):
		return apiError{http.StatusRequestEntityTooLarge, "BODY_TOO_LARGE", "request body exceeds the allowed size"}
	case errors.Is(err, errBadJSON):
		return apiError{http.StatusBadRequest, "MALFORMED_BODY", trimPrefix(err)}
	case errors.Is(err, errMissingIdempotencyKey):
		return apiError{http.StatusBadRequest, "MISSING_IDEMPOTENCY_KEY", "the Idempotency-Key header is required"}
	case errors.Is(err, errInvalidCursor):
		return apiError{http.StatusBadRequest, "INVALID_CURSOR", "the cursor is not valid"}
	case errors.Is(err, wager.ErrKindNotAllowed):
		return apiError{http.StatusBadRequest, "KIND_NOT_ALLOWED", "this kind cannot be submitted by providers"}
	case errors.Is(err, wager.ErrInvalidAmountForKind):
		return apiError{http.StatusBadRequest, "INVALID_AMOUNT_FOR_KIND", trimPrefix(err)}
	case errors.Is(err, wager.ErrInvalidReference):
		return apiError{http.StatusBadRequest, "INVALID_REFERENCE", trimPrefix(err)}
	case errors.Is(err, money.ErrScaleExceeded):
		return apiError{http.StatusBadRequest, "INVALID_MONEY_SCALE", "amounts must have exactly two decimal places"}
	case errors.Is(err, money.ErrInvalidAmount), errors.Is(err, money.ErrNegativeAmount),
		errors.Is(err, money.ErrInvalidCurrency), errors.Is(err, money.ErrOverflow):
		return apiError{http.StatusBadRequest, "INVALID_MONEY", trimPrefix(err)}
	case errors.Is(err, application.ErrInvalidInput):
		return apiError{http.StatusBadRequest, "INVALID_INPUT", trimPrefix(err)}
	case errors.Is(err, application.ErrNotFound):
		return apiError{http.StatusNotFound, "NOT_FOUND", "resource not found"}
	case errors.Is(err, wager.ErrPayloadConflict):
		return apiError{http.StatusConflict, "IDEMPOTENCY_PAYLOAD_CONFLICT", "the Idempotency-Key was already used with a different payload"}
	case errors.Is(err, wager.ErrIdempotencyKeyMismatch):
		return apiError{http.StatusConflict, "IDEMPOTENCY_KEY_MISMATCH", "this operation was already registered with another Idempotency-Key"}
	case errors.As(err, &conflict) && conflict.Constraint == "wallets_player_currency_key":
		return apiError{http.StatusConflict, "WALLET_ALREADY_EXISTS", "a wallet already exists for this player and currency"}
	case errors.Is(err, application.ErrConflict):
		return apiError{http.StatusConflict, "CONFLICT", "the request conflicts with the current state"}
	case errors.Is(err, application.ErrConcurrentModification):
		return apiError{http.StatusConflict, "CONCURRENT_MODIFICATION", "the resource changed concurrently; retry the request"}
	case errors.Is(err, ErrAuthNotReady), errors.Is(err, application.ErrUnavailable),
		errors.Is(err, context.DeadlineExceeded):
		return apiError{http.StatusServiceUnavailable, "TEMPORARILY_UNAVAILABLE", "a dependency is unavailable; retry later"}
	case errors.Is(err, context.Canceled):
		return apiError{499, "CLIENT_CLOSED_REQUEST", "the client closed the request"}
	default:
		return apiError{http.StatusInternalServerError, "INTERNAL_ERROR", "unexpected error"}
	}
}

func trimPrefix(err error) string {
	msg := err.Error()
	for _, sentinel := range []error{application.ErrInvalidInput, errBadJSON} {
		msg = strings.TrimPrefix(msg, sentinel.Error())
	}
	msg = strings.TrimPrefix(msg, ": ")
	if msg == "" {
		return "invalid input"
	}
	return msg
}

type loggerKey struct{}

func withLogger(ctx context.Context, l *slog.Logger) context.Context {
	return context.WithValue(ctx, loggerKey{}, l)
}

func logger(ctx context.Context) *slog.Logger {
	if l, ok := ctx.Value(loggerKey{}).(*slog.Logger); ok {
		return l
	}
	return slog.Default()
}
