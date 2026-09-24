package wagering_test

import (
	"bytes"
	"encoding/json"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/MurilojrMarques/backend-challenge-go/internal/application/wagering"
)

func TestFingerprintIsCanonical(t *testing.T) {
	t.Parallel()

	fp := wagering.Fingerprint{
		ProviderID:            "provider-a",
		ExternalTransactionID: "tx-1",
		PlayerID:              "0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1",
		WalletID:              "0192f291-27dd-7d3f-8071-5f8685deef37",
		RoundID:               "round-1",
		GameID:                "fortune-chimp",
		Kind:                  "BET",
		Amount:                "25.00",
		Currency:              "BRL",
	}

	raw, err := fp.Canonical()
	require.NoError(t, err)
	assert.Equal(t,
		`{"amount":"25.00","currency":"BRL","externalTransactionId":"tx-1","gameId":"fortune-chimp","kind":"BET","playerId":"0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1","providerId":"provider-a","roundId":"round-1","walletId":"0192f291-27dd-7d3f-8071-5f8685deef37"}`,
		string(raw))

	var keys []string
	dec := json.NewDecoder(bytes.NewReader(raw))
	_, err = dec.Token()
	require.NoError(t, err)
	for dec.More() {
		tok, err := dec.Token()
		require.NoError(t, err)
		keys = append(keys, tok.(string))
		var discard any
		require.NoError(t, dec.Decode(&discard))
	}
	assert.True(t, sort.StringsAreSorted(keys), "canonical JSON keys are sorted: %v", keys)

	hash, err := fp.Hash()
	require.NoError(t, err)
	assert.Len(t, hash, 64)

	again, err := fp.Hash()
	require.NoError(t, err)
	assert.Equal(t, hash, again, "hash is deterministic")

	var generic map[string]any
	require.NoError(t, json.Unmarshal(raw, &generic))
	assert.NotContains(t, generic, "idempotencyKey")
	assert.NotContains(t, generic, "messageId")
	assert.NotContains(t, generic, "referenceExternalTransactionId", "absent reference is omitted, not serialized as empty")
}

func TestFingerprintChangesWithBusinessFields(t *testing.T) {
	t.Parallel()

	base := wagering.Fingerprint{ProviderID: "p", ExternalTransactionID: "t", PlayerID: "pl", WalletID: "w", RoundID: "r", GameID: "g", Kind: "BET", Amount: "25.00", Currency: "BRL"}
	baseHash, err := base.Hash()
	require.NoError(t, err)

	mutations := map[string]func(f *wagering.Fingerprint){
		"amount":    func(f *wagering.Fingerprint) { f.Amount = "25.01" },
		"currency":  func(f *wagering.Fingerprint) { f.Currency = "USD" },
		"kind":      func(f *wagering.Fingerprint) { f.Kind = "WIN" },
		"wallet":    func(f *wagering.Fingerprint) { f.WalletID = "w2" },
		"player":    func(f *wagering.Fingerprint) { f.PlayerID = "pl2" },
		"round":     func(f *wagering.Fingerprint) { f.RoundID = "r2" },
		"game":      func(f *wagering.Fingerprint) { f.GameID = "g2" },
		"reference": func(f *wagering.Fingerprint) { f.ReferenceExternalTransactionID = "t0" },
	}
	for name, mutate := range mutations {
		f := base
		mutate(&f)
		h, err := f.Hash()
		require.NoError(t, err)
		assert.NotEqual(t, baseHash, h, "changing %s must change the hash", name)
	}
}
