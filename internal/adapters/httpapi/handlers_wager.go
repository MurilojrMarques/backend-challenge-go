package httpapi

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"

	"github.com/MurilojrMarques/backend-challenge-go/internal/application"
	"github.com/MurilojrMarques/backend-challenge-go/internal/application/wagering"
	"github.com/MurilojrMarques/backend-challenge-go/internal/domain/wager"
)

const IdempotencyKeyHeader = "Idempotency-Key"

type wagerHandler struct {
	service      *wagering.Service
	maxBodyBytes int64
}

func cleanText(s string) bool {
	return utf8.ValidString(s) && !strings.ContainsFunc(s, unicode.IsControl)
}

func pathString(r *http.Request, name string) (string, error) {
	value, err := url.PathUnescape(chi.URLParam(r, name))
	if err != nil || !cleanText(value) {
		return "", fmt.Errorf("%w: %s is not a valid path segment", application.ErrInvalidInput, name)
	}
	return value, nil
}

func (h *wagerHandler) submit(w http.ResponseWriter, r *http.Request) {
	principal, err := principalFrom(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	key := strings.TrimSpace(r.Header.Get(IdempotencyKeyHeader))
	if key == "" {
		writeError(w, r, errMissingIdempotencyKey)
		return
	}
	if !cleanText(key) {
		writeError(w, r, fmt.Errorf("%w: %s must be valid UTF-8 without control characters", application.ErrInvalidInput, IdempotencyKeyHeader))
		return
	}
	var req SubmitWagerRequest
	if err := decodeJSON(w, r, h.maxBodyBytes, &req); err != nil {
		writeError(w, r, err)
		return
	}
	res, err := h.service.Submit(r.Context(), principal, req.command(key, correlationFrom(r.Context())))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, statusFor(res.Status), wagerResultResponse(res))
}

func statusFor(s wager.Status) int {
	switch s {
	case wager.Rejected:
		return http.StatusUnprocessableEntity
	case wager.PendingReference, wager.Pending:
		return http.StatusAccepted
	default:
		return http.StatusOK
	}
}

func (h *wagerHandler) getByID(w http.ResponseWriter, r *http.Request) {
	principal, err := principalFrom(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	id, err := pathUUID("transactionId", chi.URLParam(r, "transactionId"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	view, err := h.service.GetByID(r.Context(), principal, id)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, wagerViewResponse(view))
}

func (h *wagerHandler) getByExternalID(w http.ResponseWriter, r *http.Request) {
	principal, err := principalFrom(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	providerID, err := pathString(r, "providerId")
	if err != nil {
		writeError(w, r, err)
		return
	}
	externalID, err := pathString(r, "externalTransactionId")
	if err != nil {
		writeError(w, r, err)
		return
	}
	if len(providerID) > wager.MaxFieldLength || len(externalID) > wager.MaxFieldLength {
		writeError(w, r, application.ErrNotFound)
		return
	}
	view, err := h.service.GetByExternalID(r.Context(), principal, providerID, externalID)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, wagerViewResponse(view))
}
