package httpapi

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

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
	if walletID, err := uuid.Parse(req.WalletID); err == nil {
		annotate(r.Context(), "walletId", walletID.String())
	}
	if len(req.ExternalTransactionID) <= wager.MaxFieldLength && cleanText(req.ExternalTransactionID) {
		annotate(r.Context(), "externalTransactionId", req.ExternalTransactionID)
	}
	res, err := h.service.Submit(r.Context(), principal, req.command(key, correlationFrom(r.Context())))
	if err != nil {
		writeError(w, r, err)
		return
	}
	annotateResult(r, res.TransactionID, res.Status, res.IdempotentReplay)
	writeJSON(w, statusFor(res.Status), wagerResultResponse(res))
}

func annotateResult(r *http.Request, transactionID uuid.UUID, status wager.Status, replay bool) {
	annotate(r.Context(), "transactionId", transactionID.String(), "wagerStatus", status.String(), "idempotentReplay", replay)
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
	annotate(r.Context(), "transactionId", id.String())
	view, err := h.service.GetByID(r.Context(), principal, id)
	if err != nil {
		writeError(w, r, err)
		return
	}
	annotate(r.Context(), "walletId", view.WalletID.String())
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
	annotate(r.Context(), "externalTransactionId", externalID)
	view, err := h.service.GetByExternalID(r.Context(), principal, providerID, externalID)
	if err != nil {
		writeError(w, r, err)
		return
	}
	annotate(r.Context(), "transactionId", view.ID.String(), "walletId", view.WalletID.String())
	writeJSON(w, http.StatusOK, wagerViewResponse(view))
}
