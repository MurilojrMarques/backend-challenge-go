package wager_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/MurilojrMarques/backend-challenge-go/internal/domain/money"
	"github.com/MurilojrMarques/backend-challenge-go/internal/domain/wager"
	"github.com/MurilojrMarques/backend-challenge-go/internal/domain/wallet"
)

func TestDecisionString(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "PROCEED", wager.Proceed.String())
	assert.Equal(t, "AWAIT", wager.Await.String())
	assert.Equal(t, "REJECT", wager.Reject.String())
	assert.Equal(t, "UNKNOWN", wager.Decision(99).String())
}

func TestEvaluate(t *testing.T) {
	t.Parallel()

	f := newFixture()
	other := newFixture()

	rich := f.wallet(t, brl("1000.00"))
	poor := f.wallet(t, brl("10.00"))
	maxBalance, err := money.FromUnits(money.MaxUnits, money.BRL)
	require.NoError(t, err)
	full := f.wallet(t, maxBalance)
	foreign := other.wallet(t, brl("1000.00"))
	usdWallet, _, err := wallet.Open(wallet.OpenParams{ID: f.walletID, PlayerID: f.playerID, InitialBalance: money.Zero(usd), Now: now})
	require.NoError(t, err)

	bet := f.processedBet(t, brl("25.00"), "bet-1")
	win := f.processed(t, wager.Win, brl("40.00"), "win-1", "")
	refund := f.processed(t, wager.Refund, brl("25.00"), "refund-1", "bet-1")
	rollback := f.processed(t, wager.Rollback, brl("25.00"), "rb-1", "bet-1")
	pendingBet := f.mustExternal(t, wager.Bet, brl("25.00"), "bet-pending", "")
	rejectedBet := f.rejected(t, wager.Bet, brl("25.00"), "bet-rejected", "", wager.InsufficientFunds)
	foreignBet := other.processedBet(t, brl("25.00"), "bet-w")

	otherProviderBet := func() *wager.Transaction {
		p := f.external(wager.Bet, brl("25.00"), "bet-b", "")
		p.External.ProviderID, p.External.IdempotencyKey = "provider-b", "provider-b:bet-b"
		tx, err := wager.NewExternal(p)
		require.NoError(t, err)
		require.NoError(t, tx.MarkProcessed(brl("900.00"), now))
		return tx
	}()
	otherRoundBet := func() *wager.Transaction {
		p := f.external(wager.Bet, brl("25.00"), "bet-r", "")
		p.External.RoundID = "round-2"
		tx, err := wager.NewExternal(p)
		require.NoError(t, err)
		require.NoError(t, tx.MarkProcessed(brl("900.00"), now))
		return tx
	}()

	proceed := func(d wallet.Direction) wager.Verdict {
		return wager.Verdict{Decision: wager.Proceed, Moves: true, Direction: d}
	}
	reject := func(c wager.FailureCode) wager.Verdict {
		return wager.Verdict{Decision: wager.Reject, Code: c}
	}
	await := wager.Verdict{Decision: wager.Await}
	withRef := func(tx *wager.Transaction) wager.Reference { return wager.Reference{Transaction: tx} }
	reversed := func(tx *wager.Transaction) wager.Reference {
		return wager.Reference{Transaction: tx, AlreadyReversed: true}
	}

	cases := map[string]struct {
		tx     *wager.Transaction
		wallet *wallet.Wallet
		ref    wager.Reference
		want   wager.Verdict
	}{
		"bet proceeds":               {f.mustExternal(t, wager.Bet, brl("10.00"), "x1", ""), rich, wager.Reference{}, proceed(wallet.Debit)},
		"bet exactly balance":        {f.mustExternal(t, wager.Bet, brl("1000.00"), "x2", ""), rich, wager.Reference{}, proceed(wallet.Debit)},
		"bet above balance":          {f.mustExternal(t, wager.Bet, brl("1000.01"), "x3", ""), rich, wager.Reference{}, reject(wager.InsufficientFunds)},
		"loss proceeds without move": {f.mustExternal(t, wager.Loss, brl("0.00"), "x4", ""), rich, wager.Reference{}, wager.Verdict{Decision: wager.Proceed}},
		"win proceeds":               {f.mustExternal(t, wager.Win, brl("10.00"), "x5", ""), rich, wager.Reference{}, proceed(wallet.Credit)},
		"win overflows balance":      {f.mustExternal(t, wager.Win, brl("0.01"), "x6", ""), full, wager.Reference{}, reject(wager.BalanceLimitExceeded)},
		"wallet of another player":   {f.mustExternal(t, wager.Bet, brl("10.00"), "x7", ""), foreign, wager.Reference{}, reject(wager.WalletPlayerMismatch)},
		"wallet in other currency":   {f.mustExternal(t, wager.Bet, brl("10.00"), "x8", ""), usdWallet, wager.Reference{}, reject(wager.CurrencyMismatch)},
		"ownership checked first":    {f.mustExternal(t, wager.Rollback, brl("25.00"), "x9", "bet-1"), foreign, wager.Reference{}, reject(wager.WalletPlayerMismatch)},

		"reference missing":        {f.mustExternal(t, wager.Rollback, brl("25.00"), "x10", "bet-1"), rich, wager.Reference{}, await},
		"reference still pending":  {f.mustExternal(t, wager.Rollback, brl("25.00"), "x11", "bet-pending"), rich, withRef(pendingBet), await},
		"reference rejected":       {f.mustExternal(t, wager.Rollback, brl("25.00"), "x12", "bet-rejected"), rich, withRef(rejectedBet), reject(wager.ReferenceNotProcessed)},
		"refund of a win":          {f.mustExternal(t, wager.Refund, brl("40.00"), "x13", "win-1"), rich, withRef(win), reject(wager.ReferenceKindNotAllowed)},
		"rollback of a rollback":   {f.mustExternal(t, wager.Rollback, brl("25.00"), "x14", "rb-1"), rich, withRef(rollback), reject(wager.ReferenceKindNotAllowed)},
		"reference other provider": {f.mustExternal(t, wager.Rollback, brl("25.00"), "x15", "bet-b"), rich, withRef(otherProviderBet), reject(wager.ReferenceMismatch)},
		"reference other round":    {f.mustExternal(t, wager.Rollback, brl("25.00"), "x16", "bet-r"), rich, withRef(otherRoundBet), reject(wager.ReferenceMismatch)},
		"reference other wallet":   {f.mustExternal(t, wager.Rollback, brl("25.00"), "x17", "bet-w"), rich, withRef(foreignBet), reject(wager.ReferenceMismatch)},
		"refund amount differs":    {f.mustExternal(t, wager.Refund, brl("20.00"), "x18", "bet-1"), rich, withRef(bet), reject(wager.ReferenceAmountMismatch)},
		"refund already reversed":  {f.mustExternal(t, wager.Refund, brl("25.00"), "x19", "bet-1"), rich, reversed(bet), reject(wager.ReferenceAlreadyReversed)},
		"rollback after refund":    {f.mustExternal(t, wager.Rollback, brl("25.00"), "x20", "bet-1"), rich, reversed(bet), reject(wager.ReferenceAlreadyReversed)},

		"refund credits":              {f.mustExternal(t, wager.Refund, brl("25.00"), "x21", "bet-1"), rich, withRef(bet), proceed(wallet.Credit)},
		"rollback of bet credits":     {f.mustExternal(t, wager.Rollback, brl("25.00"), "x22", "bet-1"), rich, withRef(bet), proceed(wallet.Credit)},
		"rollback of win debits":      {f.mustExternal(t, wager.Rollback, brl("40.00"), "x23", "win-1"), rich, withRef(win), proceed(wallet.Debit)},
		"rollback of refund debits":   {f.mustExternal(t, wager.Rollback, brl("25.00"), "x24", "refund-1"), rich, withRef(refund), proceed(wallet.Debit)},
		"rollback of win no funds":    {f.mustExternal(t, wager.Rollback, brl("40.00"), "x25", "win-1"), poor, withRef(win), reject(wager.ReversalInsufficientFunds)},
		"rollback of refund no funds": {f.mustExternal(t, wager.Rollback, brl("25.00"), "x26", "refund-1"), poor, withRef(refund), reject(wager.ReversalInsufficientFunds)},
		"win with ref other amount":   {f.mustExternal(t, wager.Win, brl("100.00"), "x27", "bet-1"), rich, reversed(bet), proceed(wallet.Credit)},
	}

	for name, tc := range cases {
		assert.Equal(t, tc.want, wager.Evaluate(tc.tx, tc.wallet, tc.ref), name)
	}

	assert.True(t, brl("1000.00").Equal(rich.Balance()), "Evaluate never mutates the wallet")
	assert.Equal(t, wallet.InitialVersion, rich.Version())
}

func TestReferencePolicy(t *testing.T) {
	t.Parallel()

	f := newFixture()
	tx := f.mustExternal(t, wager.Rollback, brl("25.00"), "r1", "bet-1")
	policy := wager.ReferencePolicy{MaxAttempts: 3, TTL: time.Hour}

	assert.False(t, policy.Exhausted(tx, now))
	assert.False(t, policy.Exhausted(tx, now.Add(59*time.Minute)))
	assert.True(t, policy.Exhausted(tx, now.Add(time.Hour)))

	require.NoError(t, tx.AwaitReference(now))
	require.NoError(t, tx.AwaitReference(now))
	assert.False(t, policy.Exhausted(tx, now))
	require.NoError(t, tx.AwaitReference(now))
	assert.True(t, policy.Exhausted(tx, now))

	assert.False(t, wager.ReferencePolicy{}.Exhausted(tx, now.Add(24*time.Hour)))
}
