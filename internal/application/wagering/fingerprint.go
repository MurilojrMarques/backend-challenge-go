package wagering

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/MurilojrMarques/backend-challenge-go/internal/application"
)

type Fingerprint struct {
	Amount                         string `json:"amount"`
	Currency                       string `json:"currency"`
	ExternalTransactionID          string `json:"externalTransactionId"`
	GameID                         string `json:"gameId"`
	Kind                           string `json:"kind"`
	PlayerID                       string `json:"playerId"`
	ProviderID                     string `json:"providerId"`
	ReferenceExternalTransactionID string `json:"referenceExternalTransactionId,omitempty"`
	RoundID                        string `json:"roundId"`
	WalletID                       string `json:"walletId"`
}

func (f Fingerprint) Canonical() ([]byte, error) {
	raw, err := json.Marshal(f)
	if err != nil {
		return nil, fmt.Errorf("%w: canonical payload: %v", application.ErrInvalidInput, err)
	}
	return raw, nil
}

func (f Fingerprint) Hash() (string, error) {
	raw, err := f.Canonical()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}
