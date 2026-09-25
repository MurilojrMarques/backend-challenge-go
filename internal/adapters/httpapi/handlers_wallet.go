package httpapi

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/MurilojrMarques/backend-challenge-go/internal/application"
	"github.com/MurilojrMarques/backend-challenge-go/internal/application/wallets"
)

type walletHandler struct {
	service      *wallets.Service
	maxBodyBytes int64
}

func (h *walletHandler) open(w http.ResponseWriter, r *http.Request) {
	principal, err := principalFrom(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	var req OpenWalletRequest
	if err := decodeJSON(w, r, h.maxBodyBytes, &req); err != nil {
		writeError(w, r, err)
		return
	}
	view, err := h.service.Open(r.Context(), principal, wallets.OpenCommand{
		PlayerID:       req.PlayerID,
		InitialBalance: application.MoneyInput{Amount: req.InitialBalance.Amount, Currency: req.InitialBalance.Currency},
		CorrelationID:  correlationFrom(r.Context()),
	})
	if err != nil {
		writeError(w, r, err)
		return
	}
	w.Header().Set("Location", "/wallets/"+view.ID.String())
	writeJSON(w, http.StatusCreated, walletResponse(view))
}

func (h *walletHandler) get(w http.ResponseWriter, r *http.Request) {
	principal, err := principalFrom(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	walletID, err := pathUUID("walletId", chi.URLParam(r, "walletId"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	view, err := h.service.Get(r.Context(), principal, walletID)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, walletResponse(view))
}

func (h *walletHandler) ledger(w http.ResponseWriter, r *http.Request) {
	principal, err := principalFrom(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	walletID, err := pathUUID("walletId", chi.URLParam(r, "walletId"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	cursor, err := decodeCursor(r.URL.Query().Get("cursor"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	limit := 0
	if raw := r.URL.Query().Get("limit"); raw != "" {
		limit, err = strconv.Atoi(raw)
		if err != nil || limit < 1 {
			writeError(w, r, application.ErrInvalidInput)
			return
		}
	}
	page, err := h.service.Ledger(r.Context(), principal, walletID, cursor, limit)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, ledgerResponse(walletID, page))
}

func (h *walletHandler) reconcile(w http.ResponseWriter, r *http.Request) {
	principal, err := principalFrom(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	walletID, err := pathUUID("walletId", chi.URLParam(r, "walletId"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	rec, err := h.service.Reconcile(r.Context(), principal, walletID)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, reconciliationResponse(rec))
}
