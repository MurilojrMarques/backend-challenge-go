package wager_test

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/MurilojrMarques/backend-challenge-go/internal/domain/money"
	"github.com/MurilojrMarques/backend-challenge-go/internal/domain/wager"
)

func TestParsers(t *testing.T) {
	t.Parallel()

	for _, s := range []string{"OPENING", "BET", "WIN", "LOSS", "REFUND", "ROLLBACK"} {
		k, err := wager.ParseKind(s)
		require.NoError(t, err)
		assert.Equal(t, s, k.String())
	}
	for _, s := range []string{"", "bet", "Bet", "DEPOSIT"} {
		_, err := wager.ParseKind(s)
		assert.ErrorIs(t, err, wager.ErrInvalidTransaction, s)
	}

	for _, s := range []string{"PENDING", "PENDING_REFERENCE", "PROCESSED", "REJECTED", "FAILED"} {
		st, err := wager.ParseStatus(s)
		require.NoError(t, err)
		assert.Equal(t, s, st.String())
	}
	for _, s := range []string{"", "pending", "DONE"} {
		_, err := wager.ParseStatus(s)
		assert.ErrorIs(t, err, wager.ErrInvalidTransaction, s)
	}

	code, err := wager.ParseFailureCode("INSUFFICIENT_FUNDS")
	require.NoError(t, err)
	assert.Equal(t, wager.InsufficientFunds, code)
	_, err = wager.ParseFailureCode("NOPE")
	assert.ErrorIs(t, err, wager.ErrInvalidFailureCode)
}

func TestKindPredicates(t *testing.T) {
	t.Parallel()

	cases := []struct {
		kind                                             wager.Kind
		external, reversal, allowsRef, zeroAmount, moves bool
	}{
		{wager.Opening, false, false, false, false, true},
		{wager.Bet, true, false, false, false, true},
		{wager.Win, true, false, true, false, true},
		{wager.Loss, true, false, false, true, false},
		{wager.Refund, true, true, true, false, true},
		{wager.Rollback, true, true, true, false, true},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.external, tc.kind.External(), "%s External", tc.kind)
		assert.Equal(t, tc.reversal, tc.kind.IsReversal(), "%s IsReversal", tc.kind)
		assert.Equal(t, tc.allowsRef, tc.kind.AllowsReference(), "%s AllowsReference", tc.kind)
		assert.Equal(t, tc.zeroAmount, tc.kind.RequiresZeroAmount(), "%s RequiresZeroAmount", tc.kind)
		assert.Equal(t, tc.moves, tc.kind.MovesBalance(), "%s MovesBalance", tc.kind)
	}

	allowed := map[wager.Kind][]wager.Kind{
		wager.Win:      {wager.Bet},
		wager.Refund:   {wager.Bet},
		wager.Rollback: {wager.Bet, wager.Win, wager.Refund},
	}
	all := []wager.Kind{wager.Opening, wager.Bet, wager.Win, wager.Loss, wager.Refund, wager.Rollback}
	for _, from := range all {
		for _, to := range all {
			want := false
			for _, ok := range allowed[from] {
				want = want || ok == to
			}
			assert.Equal(t, want, from.CanReference(to), "%s -> %s", from, to)
		}
	}
}

func TestStatusTransitions(t *testing.T) {
	t.Parallel()

	all := []wager.Status{wager.Pending, wager.PendingReference, wager.Processed, wager.Rejected, wager.Failed}
	allowed := map[wager.Status][]wager.Status{
		wager.Pending:          {wager.PendingReference, wager.Processed, wager.Rejected, wager.Failed},
		wager.PendingReference: {wager.Processed, wager.Rejected, wager.Failed},
	}
	for _, from := range all {
		for _, to := range all {
			want := false
			for _, ok := range allowed[from] {
				want = want || ok == to
			}
			assert.Equal(t, want, from.CanTransitionTo(to), "%s -> %s", from, to)
		}
		assert.Equal(t, len(allowed[from]) == 0, from.Terminal(), "%s Terminal", from)
	}
}

func TestFailureCodeClassification(t *testing.T) {
	t.Parallel()

	correctable := []wager.FailureCode{
		wager.CurrencyMismatch, wager.WalletPlayerMismatch, wager.ReferenceKindNotAllowed,
		wager.ReferenceMismatch, wager.ReferenceAmountMismatch,
	}
	definitive := []wager.FailureCode{
		wager.InsufficientFunds, wager.ReversalInsufficientFunds, wager.BalanceLimitExceeded,
		wager.ReferenceNotFound, wager.ReferenceNotProcessed, wager.ReferenceAlreadyReversed,
	}
	for _, c := range correctable {
		assert.True(t, c.Correctable(), c)
		assert.True(t, c.Rejection(), c)
	}
	for _, c := range definitive {
		assert.False(t, c.Correctable(), c)
		assert.True(t, c.Rejection(), c)
	}
	assert.True(t, wager.PermanentFailure.Valid())
	assert.False(t, wager.PermanentFailure.Rejection())
	assert.False(t, wager.FailureCode("NOPE").Valid())
}

func TestNewExternal(t *testing.T) {
	t.Parallel()

	f := newFixture()
	p := f.external(wager.Bet, brl("25.00"), "tx-1", "")
	tx, err := wager.NewExternal(p)
	require.NoError(t, err)

	assert.Equal(t, p.ID, tx.ID())
	assert.Equal(t, wager.Bet, tx.Kind())
	assert.Equal(t, wager.Pending, tx.Status())
	assert.Equal(t, f.walletID, tx.WalletID())
	assert.Equal(t, f.playerID, tx.PlayerID())
	assert.True(t, brl("25.00").Equal(tx.Amount()))
	assert.Equal(t, now.UTC(), tx.CreatedAt())
	assert.Equal(t, now.UTC(), tx.UpdatedAt())
	assert.False(t, tx.Terminal())
	assert.False(t, tx.HasReference())

	ext, ok := tx.External()
	require.True(t, ok)
	assert.Equal(t, p.External, ext)

	_, ok = tx.BalanceAfter()
	assert.False(t, ok)
	_, ok = tx.FailureCode()
	assert.False(t, ok)
	_, ok = tx.CompletedAt()
	assert.False(t, ok)
	_, ok = tx.ResolvedReferenceID()
	assert.False(t, ok)
	_, _, ok = tx.ReferenceKey()
	assert.False(t, ok)

	rollback := f.mustExternal(t, wager.Rollback, brl("25.00"), "tx-2", "tx-1")
	provider, ref, ok := rollback.ReferenceKey()
	require.True(t, ok)
	assert.Equal(t, "provider-a", provider)
	assert.Equal(t, "tx-1", ref)
}

func TestNewExternalRejects(t *testing.T) {
	t.Parallel()

	f := newFixture()
	negative, err := money.FromUnits(-1, money.BRL)
	require.NoError(t, err)
	tooLong := strings.Repeat("x", wager.MaxFieldLength+1)

	cases := map[string]struct {
		mutate func(p *wager.ExternalParams)
		want   error
	}{
		"opening via external":  {func(p *wager.ExternalParams) { p.Kind = wager.Opening }, wager.ErrKindNotAllowed},
		"unknown kind":          {func(p *wager.ExternalParams) { p.Kind = "DEPOSIT" }, wager.ErrInvalidTransaction},
		"missing id":            {func(p *wager.ExternalParams) { p.ID = uuid.Nil }, wager.ErrInvalidTransaction},
		"missing wallet":        {func(p *wager.ExternalParams) { p.WalletID = uuid.Nil }, wager.ErrInvalidTransaction},
		"missing player":        {func(p *wager.ExternalParams) { p.PlayerID = uuid.Nil }, wager.ErrInvalidTransaction},
		"missing timestamp":     {func(p *wager.ExternalParams) { p.Now = time.Time{} }, wager.ErrInvalidTransaction},
		"uninitialized amount":  {func(p *wager.ExternalParams) { p.Amount = money.Money{} }, wager.ErrInvalidTransaction},
		"negative amount":       {func(p *wager.ExternalParams) { p.Amount = negative }, wager.ErrInvalidAmountForKind},
		"bet with zero":         {func(p *wager.ExternalParams) { p.Amount = brl("0.00") }, wager.ErrInvalidAmountForKind},
		"bet with reference":    {func(p *wager.ExternalParams) { p.External.ReferenceExternalTransactionID = "tx-0" }, wager.ErrInvalidReference},
		"empty provider":        {func(p *wager.ExternalParams) { p.External.ProviderID = "" }, wager.ErrInvalidTransaction},
		"padded provider":       {func(p *wager.ExternalParams) { p.External.ProviderID = " provider-a" }, wager.ErrInvalidTransaction},
		"provider too long":     {func(p *wager.ExternalParams) { p.External.ProviderID = tooLong }, wager.ErrInvalidTransaction},
		"empty external id":     {func(p *wager.ExternalParams) { p.External.ExternalTransactionID = "" }, wager.ErrInvalidTransaction},
		"external id too long":  {func(p *wager.ExternalParams) { p.External.ExternalTransactionID = tooLong }, wager.ErrInvalidTransaction},
		"empty idempotency key": {func(p *wager.ExternalParams) { p.External.IdempotencyKey = "" }, wager.ErrInvalidTransaction},
		"empty hash":            {func(p *wager.ExternalParams) { p.External.PayloadHash = "" }, wager.ErrInvalidTransaction},
		"empty round":           {func(p *wager.ExternalParams) { p.External.RoundID = "" }, wager.ErrInvalidTransaction},
		"empty game":            {func(p *wager.ExternalParams) { p.External.GameID = "" }, wager.ErrInvalidTransaction},
	}
	for name, tc := range cases {
		p := f.external(wager.Bet, brl("25.00"), "tx-1", "")
		tc.mutate(&p)
		tx, err := wager.NewExternal(p)
		assert.ErrorIs(t, err, tc.want, name)
		assert.Nil(t, tx, name)
	}

	atLimit := f.external(wager.Bet, brl("25.00"), "tx-1", "")
	atLimit.External.ProviderID = strings.Repeat("x", wager.MaxFieldLength)
	_, err = wager.NewExternal(atLimit)
	assert.NoError(t, err, "field exactly at the limit is accepted")

	referenceCases := map[string]struct {
		kind      wager.Kind
		reference string
		want      error
	}{
		"refund without reference":   {wager.Refund, "", wager.ErrInvalidReference},
		"rollback without reference": {wager.Rollback, "", wager.ErrInvalidReference},
		"loss with reference":        {wager.Loss, "tx-0", wager.ErrInvalidReference},
		"self reference":             {wager.Rollback, "tx-1", wager.ErrInvalidReference},
		"padded reference":           {wager.Rollback, "tx-0 ", wager.ErrInvalidReference},
		"reference too long":         {wager.Rollback, tooLong, wager.ErrInvalidReference},
	}
	for name, tc := range referenceCases {
		amount := brl("25.00")
		if tc.kind == wager.Loss {
			amount = brl("0.00")
		}
		_, err := wager.NewExternal(f.external(tc.kind, amount, "tx-1", tc.reference))
		assert.ErrorIs(t, err, tc.want, name)
	}
}

func TestZeroAmountPolicy(t *testing.T) {
	t.Parallel()

	f := newFixture()
	cases := []struct {
		kind       wager.Kind
		reference  string
		zeroOK     bool
		positiveOK bool
	}{
		{wager.Bet, "", false, true},
		{wager.Win, "", false, true},
		{wager.Loss, "", true, false},
		{wager.Refund, "tx-0", false, true},
		{wager.Rollback, "tx-0", false, true},
	}
	for _, tc := range cases {
		_, err := wager.NewExternal(f.external(tc.kind, brl("0.00"), "tx-1", tc.reference))
		assert.Equal(t, tc.zeroOK, err == nil, "%s with 0.00: %v", tc.kind, err)
		if !tc.zeroOK {
			assert.ErrorIs(t, err, wager.ErrInvalidAmountForKind, tc.kind)
		}

		_, err = wager.NewExternal(f.external(tc.kind, brl("10.00"), "tx-1", tc.reference))
		assert.Equal(t, tc.positiveOK, err == nil, "%s with 10.00: %v", tc.kind, err)
		if !tc.positiveOK {
			assert.ErrorIs(t, err, wager.ErrInvalidAmountForKind, tc.kind)
		}
	}

	_, err := wager.NewOpening(wager.OpeningParams{ID: newID(), WalletID: f.walletID, PlayerID: f.playerID, Amount: brl("0.00"), Now: now})
	assert.ErrorIs(t, err, wager.ErrInvalidAmountForKind)
}

func TestNewOpening(t *testing.T) {
	t.Parallel()

	f := newFixture()
	p := wager.OpeningParams{ID: newID(), WalletID: f.walletID, PlayerID: f.playerID, Amount: brl("1000.00"), Now: now}
	tx, err := wager.NewOpening(p)
	require.NoError(t, err)

	assert.Equal(t, wager.Opening, tx.Kind())
	assert.Equal(t, wager.Processed, tx.Status())
	assert.True(t, tx.Terminal())
	balance, ok := tx.BalanceAfter()
	require.True(t, ok)
	assert.True(t, brl("1000.00").Equal(balance))
	completed, ok := tx.CompletedAt()
	require.True(t, ok)
	assert.Equal(t, now.UTC(), completed)
	_, ok = tx.External()
	assert.False(t, ok)

	assert.ErrorIs(t, tx.CheckReplay("k", "h"), wager.ErrInvalidTransaction)

	_, err = wager.NewOpening(wager.OpeningParams{ID: uuid.Nil, WalletID: f.walletID, PlayerID: f.playerID, Amount: brl("1.00"), Now: now})
	assert.ErrorIs(t, err, wager.ErrInvalidTransaction)
}

func TestMarkProcessed(t *testing.T) {
	t.Parallel()

	f := newFixture()
	tx := f.mustExternal(t, wager.Bet, brl("25.00"), "tx-1", "")
	later := now.Add(time.Second)

	usdBalance, err := money.FromUnits(97500, usd)
	require.NoError(t, err)
	assert.ErrorIs(t, tx.MarkProcessed(usdBalance, later), wager.ErrInvalidTransaction)
	assert.ErrorIs(t, tx.MarkProcessed(money.Money{}, later), wager.ErrInvalidTransaction)
	assert.ErrorIs(t, tx.MarkProcessed(brl("975.00"), time.Time{}), wager.ErrInvalidTransaction)
	assert.Equal(t, wager.Pending, tx.Status())

	require.NoError(t, tx.MarkProcessed(brl("975.00"), later))
	assert.Equal(t, wager.Processed, tx.Status())
	assert.True(t, tx.Terminal())
	balance, ok := tx.BalanceAfter()
	require.True(t, ok)
	assert.True(t, brl("975.00").Equal(balance))
	completed, ok := tx.CompletedAt()
	require.True(t, ok)
	assert.Equal(t, later.UTC(), completed)
	assert.Equal(t, later.UTC(), tx.UpdatedAt())
	assert.Equal(t, now.UTC(), tx.CreatedAt())
}

func TestRejectAndFail(t *testing.T) {
	t.Parallel()

	f := newFixture()
	observed := brl("100.00")

	rejected := f.mustExternal(t, wager.Bet, brl("125.00"), "tx-1", "")
	assert.ErrorIs(t, rejected.Reject(wager.PermanentFailure, observed, now), wager.ErrInvalidFailureCode)
	assert.ErrorIs(t, rejected.Reject("NOPE", observed, now), wager.ErrInvalidFailureCode)
	assert.ErrorIs(t, rejected.Reject(wager.InsufficientFunds, money.Money{}, now), wager.ErrInvalidTransaction)
	assert.ErrorIs(t, rejected.Reject(wager.InsufficientFunds, observed, time.Time{}), wager.ErrInvalidTransaction)
	assert.ErrorIs(t, rejected.Fail(time.Time{}), wager.ErrInvalidTransaction)
	assert.Equal(t, wager.Pending, rejected.Status())

	require.NoError(t, rejected.Reject(wager.InsufficientFunds, observed, now))
	assert.Equal(t, wager.Rejected, rejected.Status())
	code, ok := rejected.FailureCode()
	require.True(t, ok)
	assert.Equal(t, wager.InsufficientFunds, code)
	balance, ok := rejected.BalanceAfter()
	require.True(t, ok)
	assert.True(t, observed.Equal(balance), "rejection records the observed balance")
	_, ok = rejected.CompletedAt()
	assert.True(t, ok)

	failed := f.mustExternal(t, wager.Bet, brl("25.00"), "tx-2", "")
	require.NoError(t, failed.Fail(now))
	assert.Equal(t, wager.Failed, failed.Status())
	code, _ = failed.FailureCode()
	assert.Equal(t, wager.PermanentFailure, code)
	_, ok = failed.BalanceAfter()
	assert.False(t, ok, "failure records no balance")
}

func TestTerminalIsImmutable(t *testing.T) {
	t.Parallel()

	f := newFixture()
	for _, terminal := range []*wager.Transaction{
		f.processedBet(t, brl("25.00"), "tx-1"),
		f.rejected(t, wager.Rollback, brl("25.00"), "tx-2", "tx-1", wager.ReferenceNotFound),
		func() *wager.Transaction {
			tx := f.mustExternal(t, wager.Rollback, brl("25.00"), "tx-3", "tx-1")
			require.NoError(t, tx.Fail(now))
			return tx
		}(),
	} {
		before := terminal.Snapshot()
		assert.ErrorIs(t, terminal.MarkProcessed(brl("1.00"), now), wager.ErrInvalidTransition)
		assert.ErrorIs(t, terminal.Reject(wager.InsufficientFunds, brl("1.00"), now), wager.ErrInvalidTransition)
		assert.ErrorIs(t, terminal.Fail(now), wager.ErrInvalidTransition)
		if terminal.HasReference() {
			assert.ErrorIs(t, terminal.AwaitReference(now), wager.ErrInvalidTransition)
			assert.ErrorIs(t, terminal.ResolveReference(newID(), now), wager.ErrInvalidTransition)
		}
		assert.Equal(t, before, terminal.Snapshot(), "terminal %s must not change", before.Status)
	}
}

func TestAwaitAndResolveReference(t *testing.T) {
	t.Parallel()

	f := newFixture()
	tx := f.mustExternal(t, wager.Rollback, brl("25.00"), "tx-2", "tx-1")

	assert.ErrorIs(t, tx.MarkProcessed(brl("925.00"), now), wager.ErrInvalidReference)
	assert.ErrorIs(t, tx.AwaitReference(time.Time{}), wager.ErrInvalidTransaction)
	assert.Equal(t, wager.Pending, tx.Status())
	assert.Equal(t, 0, tx.ReferenceAttempts())

	require.NoError(t, tx.AwaitReference(now))
	assert.Equal(t, wager.PendingReference, tx.Status())
	assert.Equal(t, 1, tx.ReferenceAttempts())
	assert.ErrorIs(t, tx.AwaitReference(time.Time{}), wager.ErrInvalidTransaction)
	assert.Equal(t, 1, tx.ReferenceAttempts())

	require.NoError(t, tx.AwaitReference(now.Add(time.Minute)))
	assert.Equal(t, wager.PendingReference, tx.Status())
	assert.Equal(t, 2, tx.ReferenceAttempts())
	assert.Equal(t, now.Add(time.Minute).UTC(), tx.UpdatedAt())

	assert.ErrorIs(t, tx.ResolveReference(uuid.Nil, now), wager.ErrInvalidReference)
	assert.ErrorIs(t, tx.ResolveReference(tx.ID(), now), wager.ErrInvalidReference)
	assert.ErrorIs(t, tx.ResolveReference(newID(), time.Time{}), wager.ErrInvalidTransaction)

	refID := newID()
	require.NoError(t, tx.ResolveReference(refID, now))
	resolved, ok := tx.ResolvedReferenceID()
	require.True(t, ok)
	assert.Equal(t, refID, resolved)

	require.NoError(t, tx.MarkProcessed(brl("925.00"), now))
	assert.Equal(t, wager.Processed, tx.Status())

	bet := f.mustExternal(t, wager.Bet, brl("25.00"), "tx-9", "")
	assert.ErrorIs(t, bet.AwaitReference(now), wager.ErrInvalidReference)
	assert.ErrorIs(t, bet.ResolveReference(newID(), now), wager.ErrInvalidReference)
	assert.Equal(t, wager.Pending, bet.Status())
}

func TestCheckReplay(t *testing.T) {
	t.Parallel()

	f := newFixture()
	tx := f.mustExternal(t, wager.Bet, brl("25.00"), "tx-1", "")

	assert.NoError(t, tx.CheckReplay("provider-a:tx-1", "hash-tx-1"))
	assert.ErrorIs(t, tx.CheckReplay("provider-a:tx-1", "other-hash"), wager.ErrPayloadConflict)
	assert.ErrorIs(t, tx.CheckReplay("other-key", "hash-tx-1"), wager.ErrIdempotencyKeyMismatch)
	assert.ErrorIs(t, tx.CheckReplay("other-key", "other-hash"), wager.ErrIdempotencyKeyMismatch)
}

func TestRehydrateRoundTrip(t *testing.T) {
	t.Parallel()

	f := newFixture()
	opening, err := wager.NewOpening(wager.OpeningParams{ID: newID(), WalletID: f.walletID, PlayerID: f.playerID, Amount: brl("1.00"), Now: now})
	require.NoError(t, err)

	pendingRef := f.mustExternal(t, wager.Rollback, brl("25.00"), "r1", "bet-1")
	require.NoError(t, pendingRef.AwaitReference(now))

	failed := f.mustExternal(t, wager.Bet, brl("25.00"), "b3", "")
	require.NoError(t, failed.Fail(now))

	for _, original := range []*wager.Transaction{
		opening,
		f.mustExternal(t, wager.Bet, brl("25.00"), "b1", ""),
		f.processedBet(t, brl("25.00"), "b4"),
		f.processed(t, wager.Rollback, brl("25.00"), "r2", "bet-1"),
		pendingRef,
		f.rejected(t, wager.Bet, brl("25.00"), "b2", "", wager.InsufficientFunds),
		failed,
	} {
		snapshot := original.Snapshot()
		restored, err := wager.Rehydrate(snapshot)
		require.NoError(t, err, snapshot.Kind)
		assert.Equal(t, snapshot, restored.Snapshot(), "%s/%s", snapshot.Kind, snapshot.Status)
	}

	snapshot := pendingRef.Snapshot()
	restored, err := wager.Rehydrate(snapshot)
	require.NoError(t, err)
	snapshot.External.RoundID = "mutated"
	ext, _ := restored.External()
	assert.Equal(t, "round-1", ext.RoundID, "rehydrate must copy external metadata")
}

func TestRehydrateRejectsInvalid(t *testing.T) {
	t.Parallel()

	f := newFixture()
	base := f.processedBet(t, brl("25.00"), "b1").Snapshot()
	pending := f.mustExternal(t, wager.Bet, brl("25.00"), "b2", "").Snapshot()
	ext := *base.External

	withReference := func(s wager.Snapshot) wager.Snapshot {
		e := ext
		e.ReferenceExternalTransactionID = "b0"
		s.Kind = wager.Rollback
		s.External = &e
		return s
	}

	cases := map[string]struct {
		snapshot func() wager.Snapshot
		want     error
	}{
		"unknown kind":          {func() wager.Snapshot { s := base; s.Kind = "DEPOSIT"; return s }, wager.ErrInvalidTransaction},
		"unknown status":        {func() wager.Snapshot { s := base; s.Status = "DONE"; return s }, wager.ErrInvalidTransaction},
		"external without meta": {func() wager.Snapshot { s := base; s.External = nil; return s }, wager.ErrInvalidTransaction},
		"opening with meta":     {func() wager.Snapshot { s := base; s.Kind = wager.Opening; return s }, wager.ErrInvalidTransaction},
		"loss with amount":      {func() wager.Snapshot { s := base; s.Kind = wager.Loss; return s }, wager.ErrInvalidAmountForKind},
		"external meta invalid": {func() wager.Snapshot { s := base; e := ext; e.ProviderID = ""; s.External = &e; return s }, wager.ErrInvalidTransaction},
		"processed no balance":  {func() wager.Snapshot { s := base; s.BalanceAfter = money.Money{}; return s }, wager.ErrInvalidTransaction},
		"processed with code":   {func() wager.Snapshot { s := base; s.FailureCode = wager.InsufficientFunds; return s }, wager.ErrInvalidTransaction},
		"rejected without code": {func() wager.Snapshot { s := base; s.Status = wager.Rejected; return s }, wager.ErrInvalidFailureCode},
		"rejected with permanent": {func() wager.Snapshot {
			s := base
			s.Status = wager.Rejected
			s.FailureCode = wager.PermanentFailure
			return s
		}, wager.ErrInvalidFailureCode},
		"rejected without balance": {
			func() wager.Snapshot {
				s := base
				s.Status, s.FailureCode, s.BalanceAfter = wager.Rejected, wager.InsufficientFunds, money.Money{}
				return s
			},
			wager.ErrInvalidTransaction,
		},
		"failed with other code": {func() wager.Snapshot {
			s := base
			s.Status = wager.Failed
			s.FailureCode = wager.InsufficientFunds
			return s
		}, wager.ErrInvalidFailureCode},
		"failed with balance": {func() wager.Snapshot {
			s := base
			s.Status = wager.Failed
			s.FailureCode = wager.PermanentFailure
			return s
		}, wager.ErrInvalidTransaction},
		"pending with result":    {func() wager.Snapshot { s := pending; s.BalanceAfter = brl("1.00"); return s }, wager.ErrInvalidTransaction},
		"pending with code":      {func() wager.Snapshot { s := pending; s.FailureCode = wager.InsufficientFunds; return s }, wager.ErrInvalidTransaction},
		"pending completed":      {func() wager.Snapshot { s := pending; s.CompletedAt = now; return s }, wager.ErrInvalidTransaction},
		"terminal not completed": {func() wager.Snapshot { s := base; s.CompletedAt = time.Time{}; return s }, wager.ErrInvalidTransaction},
		"completed before created": {
			func() wager.Snapshot { s := base; s.CompletedAt = s.CreatedAt.Add(-time.Second); return s },
			wager.ErrInvalidTransaction,
		},
		"updated before created": {
			func() wager.Snapshot { s := base; s.UpdatedAt = s.CreatedAt.Add(-time.Second); return s },
			wager.ErrInvalidTransaction,
		},
		"negative attempts":        {func() wager.Snapshot { s := pending; s.ReferenceAttempts = -1; return s }, wager.ErrInvalidTransaction},
		"attempts without ref":     {func() wager.Snapshot { s := pending; s.ReferenceAttempts = 1; return s }, wager.ErrInvalidReference},
		"pending ref without ref":  {func() wager.Snapshot { s := pending; s.Status = wager.PendingReference; return s }, wager.ErrInvalidReference},
		"resolved without ref":     {func() wager.Snapshot { s := pending; s.ResolvedReferenceID = newID(); return s }, wager.ErrInvalidReference},
		"processed unresolved ref": {func() wager.Snapshot { return withReference(base) }, wager.ErrInvalidReference},
		"self resolved": {
			func() wager.Snapshot { s := withReference(base); s.ResolvedReferenceID = s.ID; return s },
			wager.ErrInvalidReference,
		},
	}

	for name, tc := range cases {
		tx, err := wager.Rehydrate(tc.snapshot())
		assert.ErrorIs(t, err, tc.want, name)
		assert.Nil(t, tx, name)
	}
}
