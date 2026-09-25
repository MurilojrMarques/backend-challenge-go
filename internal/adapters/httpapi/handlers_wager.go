package httpapi

import (
	"net/http"
	"strings"

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
	providerID := chi.URLParam(r, "providerId")
	externalID := chi.URLParam(r, "externalTransactionId")
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
