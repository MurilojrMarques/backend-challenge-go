package wallet_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/MurilojrMarques/backend-challenge-go/internal/domain/money"
	"github.com/MurilojrMarques/backend-challenge-go/internal/domain/wallet"
)

var (
	now = time.Date(2026, 9, 24, 12, 0, 0, 0, time.FixedZone("BRT", -3*3600))
	usd = money.MustCurrency("USD")
)

func brl(amount string) money.Money {
	return money.MustParse(amount, money.BRL)
}

func newID() uuid.UUID {
	return uuid.Must(uuid.NewV7())
}

func openParams(initial money.Money) wallet.OpenParams {
	return wallet.OpenParams{
		ID:             newID(),
		PlayerID:       newID(),
		InitialBalance: initial,
		OpeningTxID:    newID(),
		OpeningEntryID: newID(),
		Now:            now,
	}
}

func openWith(t *testing.T, initial money.Money) *wallet.Wallet {
	t.Helper()
	w, _, err := wallet.Open(openParams(initial))
	require.NoError(t, err)
	return w
}

func movement(direction wallet.Direction, amount money.Money) wallet.Movement {
	return wallet.Movement{
		EntryID:       newID(),
		TransactionID: newID(),
		Direction:     direction,
		Amount:        amount,
		Now:           now.Add(time.Minute),
	}
}

func entryParams() wallet.LedgerEntryParams {
	return wallet.LedgerEntryParams{
		ID:            newID(),
		WalletID:      newID(),
		TransactionID: newID(),
		Direction:     wallet.Debit,
		Amount:        brl("25.00"),
		BalanceBefore: brl("100.00"),
		BalanceAfter:  brl("75.00"),
		CreatedAt:     now,
	}
}

func TestOpenWithPositiveBalance(t *testing.T) {
	t.Parallel()

	p := openParams(brl("1000.00"))
	w, entry, err := wallet.Open(p)
	require.NoError(t, err)
	require.NotNil(t, entry)

	assert.Equal(t, p.ID, w.ID())
	assert.Equal(t, p.PlayerID, w.PlayerID())
	assert.Equal(t, money.BRL, w.Currency())
	assert.True(t, brl("1000.00").Equal(w.Balance()))
	assert.Equal(t, wallet.InitialVersion, w.Version())
	assert.Equal(t, now.UTC(), w.CreatedAt())
	assert.Equal(t, now.UTC(), w.UpdatedAt())
	assert.Equal(t, time.UTC, w.CreatedAt().Location())

	assert.Equal(t, p.OpeningEntryID, entry.ID())
	assert.Equal(t, p.ID, entry.WalletID())
	assert.Equal(t, p.OpeningTxID, entry.TransactionID())
	assert.Equal(t, wallet.Credit, entry.Direction())
	assert.True(t, brl("1000.00").Equal(entry.Amount()))
	assert.True(t, brl("0.00").Equal(entry.BalanceBefore()))
	assert.True(t, brl("1000.00").Equal(entry.BalanceAfter()))
	assert.Equal(t, now.UTC(), entry.CreatedAt())
}

func TestOpenWithZeroBalance(t *testing.T) {
	t.Parallel()

	w, entry, err := wallet.Open(wallet.OpenParams{
		ID:             newID(),
		PlayerID:       newID(),
		InitialBalance: brl("0.00"),
		Now:            now,
	})
	require.NoError(t, err)
	assert.Nil(t, entry)
	assert.True(t, w.Balance().IsZero())
	assert.Equal(t, wallet.InitialVersion, w.Version())
	assert.Equal(t, money.BRL, w.Currency())
}

func TestOpenRejectsInvalidParams(t *testing.T) {
	t.Parallel()

	negative, err := money.FromUnits(-1, money.BRL)
	require.NoError(t, err)

	cases := map[string]func(p *wallet.OpenParams){
		"missing wallet id":         func(p *wallet.OpenParams) { p.ID = uuid.Nil },
		"missing player id":         func(p *wallet.OpenParams) { p.PlayerID = uuid.Nil },
		"missing timestamp":         func(p *wallet.OpenParams) { p.Now = time.Time{} },
		"uninitialized balance":     func(p *wallet.OpenParams) { p.InitialBalance = money.Money{} },
		"negative balance":          func(p *wallet.OpenParams) { p.InitialBalance = negative },
		"positive without tx id":    func(p *wallet.OpenParams) { p.OpeningTxID = uuid.Nil },
		"positive without entry id": func(p *wallet.OpenParams) { p.OpeningEntryID = uuid.Nil },
	}

	for name, mutate := range cases {
		p := openParams(brl("10.00"))
		mutate(&p)
		w, entry, err := wallet.Open(p)
		assert.ErrorIs(t, err, wallet.ErrInvalidWallet, name)
		assert.Nil(t, w, name)
		assert.Nil(t, entry, name)
	}
}

func TestApplyDebitAndCredit(t *testing.T) {
	t.Parallel()

	w := openWith(t, brl("100.00"))

	debit := movement(wallet.Debit, brl("25.00"))
	entry, err := w.Apply(debit)
	require.NoError(t, err)
	assert.Equal(t, debit.EntryID, entry.ID())
	assert.Equal(t, debit.TransactionID, entry.TransactionID())
	assert.Equal(t, w.ID(), entry.WalletID())
	assert.Equal(t, wallet.Debit, entry.Direction())
	assert.True(t, brl("100.00").Equal(entry.BalanceBefore()))
	assert.True(t, brl("75.00").Equal(entry.BalanceAfter()))
	assert.True(t, brl("75.00").Equal(w.Balance()))
	assert.Equal(t, int64(2), w.Version())
	assert.Equal(t, debit.Now.UTC(), w.UpdatedAt())
	assert.Equal(t, now.UTC(), w.CreatedAt())

	credit := movement(wallet.Credit, brl("50.00"))
	credit.Now = debit.Now.Add(time.Minute)
	entry, err = w.Apply(credit)
	require.NoError(t, err)
	assert.Equal(t, wallet.Credit, entry.Direction())
	assert.True(t, brl("75.00").Equal(entry.BalanceBefore()))
	assert.True(t, brl("125.00").Equal(entry.BalanceAfter()))
	assert.True(t, brl("125.00").Equal(w.Balance()))
	assert.Equal(t, int64(3), w.Version())
	assert.Equal(t, credit.Now.UTC(), w.UpdatedAt())
}

func TestApplyDebitToExactlyZero(t *testing.T) {
	t.Parallel()

	w := openWith(t, brl("80.00"))
	entry, err := w.Apply(movement(wallet.Debit, brl("80.00")))
	require.NoError(t, err)
	assert.True(t, entry.BalanceAfter().IsZero())
	assert.True(t, w.Balance().IsZero())
}

func TestTwoBetsOfEightyOnOneHundred(t *testing.T) {
	t.Parallel()

	w := openWith(t, brl("100.00"))

	first, err := w.Apply(movement(wallet.Debit, brl("80.00")))
	require.NoError(t, err)
	assert.True(t, brl("20.00").Equal(first.BalanceAfter()))

	_, err = w.Apply(movement(wallet.Debit, brl("80.00")))
	assert.ErrorIs(t, err, wallet.ErrInsufficientFunds)

	assert.True(t, brl("20.00").Equal(w.Balance()))
	assert.Equal(t, int64(2), w.Version())
}

func TestApplyRejectsInvalidMovement(t *testing.T) {
	t.Parallel()

	w := openWith(t, brl("100.00"))
	usdAmount, err := money.FromUnits(100, usd)
	require.NoError(t, err)

	cases := map[string]struct {
		mutate func(m *wallet.Movement)
		want   error
	}{
		"missing entry id":      {func(m *wallet.Movement) { m.EntryID = uuid.Nil }, wallet.ErrInvalidMovement},
		"missing tx id":         {func(m *wallet.Movement) { m.TransactionID = uuid.Nil }, wallet.ErrInvalidMovement},
		"missing timestamp":     {func(m *wallet.Movement) { m.Now = time.Time{} }, wallet.ErrInvalidMovement},
		"invalid direction":     {func(m *wallet.Movement) { m.Direction = "SIDEWAYS" }, wallet.ErrInvalidMovement},
		"empty direction":       {func(m *wallet.Movement) { m.Direction = "" }, wallet.ErrInvalidMovement},
		"uninitialized amount":  {func(m *wallet.Movement) { m.Amount = money.Money{} }, wallet.ErrInvalidMovement},
		"zero amount":           {func(m *wallet.Movement) { m.Amount = brl("0.00") }, wallet.ErrInvalidMovement},
		"other currency debit":  {func(m *wallet.Movement) { m.Amount = usdAmount }, wallet.ErrCurrencyMismatch},
		"other currency credit": {func(m *wallet.Movement) { m.Direction = wallet.Credit; m.Amount = usdAmount }, wallet.ErrCurrencyMismatch},
		"debit above balance":   {func(m *wallet.Movement) { m.Amount = brl("100.01") }, wallet.ErrInsufficientFunds},
	}

	for name, tc := range cases {
		m := movement(wallet.Debit, brl("1.00"))
		tc.mutate(&m)
		entry, err := w.Apply(m)
		assert.ErrorIs(t, err, tc.want, name)
		assert.Equal(t, wallet.LedgerEntry{}, entry, name)
	}

	assert.True(t, brl("100.00").Equal(w.Balance()))
	assert.Equal(t, wallet.InitialVersion, w.Version())
	assert.Equal(t, now.UTC(), w.UpdatedAt())
}

func TestApplyCreditOverflow(t *testing.T) {
	t.Parallel()

	max, err := money.FromUnits(money.MaxUnits, money.BRL)
	require.NoError(t, err)
	w := openWith(t, max)

	_, err = w.Apply(movement(wallet.Credit, brl("0.01")))
	assert.ErrorIs(t, err, money.ErrOverflow)
	assert.True(t, max.Equal(w.Balance()))
	assert.Equal(t, wallet.InitialVersion, w.Version())
}

func TestCanDebit(t *testing.T) {
	t.Parallel()

	w := openWith(t, brl("100.00"))
	usdAmount, err := money.FromUnits(100, usd)
	require.NoError(t, err)

	assert.True(t, w.CanDebit(brl("99.99")))
	assert.True(t, w.CanDebit(brl("100.00")))
	assert.False(t, w.CanDebit(brl("100.01")))
	assert.False(t, w.CanDebit(usdAmount))
	assert.False(t, w.CanDebit(money.Money{}))
	assert.True(t, brl("100.00").Equal(w.Balance()), "CanDebit is read-only")
}

func TestRehydrate(t *testing.T) {
	t.Parallel()

	original := openWith(t, brl("100.00"))
	_, err := original.Apply(movement(wallet.Debit, brl("30.00")))
	require.NoError(t, err)

	snapshot := original.Snapshot()
	restored, err := wallet.Rehydrate(snapshot)
	require.NoError(t, err)

	assert.Equal(t, snapshot, restored.Snapshot())
	assert.Equal(t, int64(2), restored.Version())
	assert.True(t, brl("70.00").Equal(restored.Balance()))
	assert.Equal(t, time.UTC, restored.CreatedAt().Location())

	entry, err := restored.Apply(movement(wallet.Debit, brl("70.00")))
	require.NoError(t, err)
	assert.True(t, entry.BalanceAfter().IsZero())
	assert.Equal(t, int64(3), restored.Version())
}

func TestRehydrateRejectsInvalidSnapshot(t *testing.T) {
	t.Parallel()

	negative, err := money.FromUnits(-1, money.BRL)
	require.NoError(t, err)

	valid := wallet.Snapshot{
		ID:        newID(),
		PlayerID:  newID(),
		Balance:   brl("10.00"),
		Version:   3,
		CreatedAt: now,
		UpdatedAt: now.Add(time.Minute),
	}

	cases := map[string]func(s *wallet.Snapshot){
		"missing wallet id":      func(s *wallet.Snapshot) { s.ID = uuid.Nil },
		"missing player id":      func(s *wallet.Snapshot) { s.PlayerID = uuid.Nil },
		"uninitialized balance":  func(s *wallet.Snapshot) { s.Balance = money.Money{} },
		"negative balance":       func(s *wallet.Snapshot) { s.Balance = negative },
		"version zero":           func(s *wallet.Snapshot) { s.Version = 0 },
		"version negative":       func(s *wallet.Snapshot) { s.Version = -1 },
		"missing createdAt":      func(s *wallet.Snapshot) { s.CreatedAt = time.Time{} },
		"missing updatedAt":      func(s *wallet.Snapshot) { s.UpdatedAt = time.Time{} },
		"updated before created": func(s *wallet.Snapshot) { s.UpdatedAt = s.CreatedAt.Add(-time.Second) },
	}

	for name, mutate := range cases {
		s := valid
		mutate(&s)
		w, err := wallet.Rehydrate(s)
		assert.ErrorIs(t, err, wallet.ErrInvalidWallet, name)
		assert.Nil(t, w, name)
	}
}

func TestParseDirection(t *testing.T) {
	t.Parallel()

	for _, s := range []string{"DEBIT", "CREDIT"} {
		d, err := wallet.ParseDirection(s)
		require.NoError(t, err)
		assert.Equal(t, s, d.String())
		assert.True(t, d.Valid())
	}
	for _, s := range []string{"", "debit", "Credit", "TRANSFER"} {
		_, err := wallet.ParseDirection(s)
		assert.ErrorIs(t, err, wallet.ErrInvalidLedgerEntry, "%q", s)
		assert.False(t, wallet.Direction(s).Valid(), "%q", s)
	}
}

func TestNewLedgerEntry(t *testing.T) {
	t.Parallel()

	debit := entryParams()
	e, err := wallet.NewLedgerEntry(debit)
	require.NoError(t, err)
	assert.Equal(t, debit.ID, e.ID())
	assert.Equal(t, debit.WalletID, e.WalletID())
	assert.Equal(t, debit.TransactionID, e.TransactionID())
	assert.Equal(t, wallet.Debit, e.Direction())
	assert.True(t, brl("25.00").Equal(e.Amount()))
	assert.True(t, brl("100.00").Equal(e.BalanceBefore()))
	assert.True(t, brl("75.00").Equal(e.BalanceAfter()))
	assert.Equal(t, now.UTC(), e.CreatedAt())

	credit := entryParams()
	credit.Direction = wallet.Credit
	credit.BalanceAfter = brl("125.00")
	_, err = wallet.NewLedgerEntry(credit)
	require.NoError(t, err)

	toZero := entryParams()
	toZero.Amount = brl("100.00")
	toZero.BalanceAfter = brl("0.00")
	_, err = wallet.NewLedgerEntry(toZero)
	require.NoError(t, err)
}

func TestNewLedgerEntryRejectsInvalid(t *testing.T) {
	t.Parallel()

	negative, err := money.FromUnits(-100, money.BRL)
	require.NoError(t, err)
	usdAmount, err := money.FromUnits(2500, usd)
	require.NoError(t, err)
	usdAfter, err := money.FromUnits(7500, usd)
	require.NoError(t, err)

	cases := map[string]struct {
		mutate func(p *wallet.LedgerEntryParams)
		want   error
	}{
		"missing entry id":        {func(p *wallet.LedgerEntryParams) { p.ID = uuid.Nil }, wallet.ErrInvalidLedgerEntry},
		"missing wallet id":       {func(p *wallet.LedgerEntryParams) { p.WalletID = uuid.Nil }, wallet.ErrInvalidLedgerEntry},
		"missing tx id":           {func(p *wallet.LedgerEntryParams) { p.TransactionID = uuid.Nil }, wallet.ErrInvalidLedgerEntry},
		"invalid direction":       {func(p *wallet.LedgerEntryParams) { p.Direction = "SIDEWAYS" }, wallet.ErrInvalidLedgerEntry},
		"missing timestamp":       {func(p *wallet.LedgerEntryParams) { p.CreatedAt = time.Time{} }, wallet.ErrInvalidLedgerEntry},
		"uninitialized amount":    {func(p *wallet.LedgerEntryParams) { p.Amount = money.Money{} }, wallet.ErrInvalidLedgerEntry},
		"zero amount":             {func(p *wallet.LedgerEntryParams) { p.Amount = brl("0.00") }, wallet.ErrInvalidLedgerEntry},
		"negative amount":         {func(p *wallet.LedgerEntryParams) { p.Amount = negative }, wallet.ErrInvalidLedgerEntry},
		"negative before":         {func(p *wallet.LedgerEntryParams) { p.BalanceBefore = negative }, wallet.ErrInvalidLedgerEntry},
		"negative after":          {func(p *wallet.LedgerEntryParams) { p.BalanceAfter = negative }, wallet.ErrInvalidLedgerEntry},
		"debit does not balance":  {func(p *wallet.LedgerEntryParams) { p.BalanceAfter = brl("80.00") }, wallet.ErrInvalidLedgerEntry},
		"credit does not balance": {func(p *wallet.LedgerEntryParams) { p.Direction = wallet.Credit }, wallet.ErrInvalidLedgerEntry},
		"after in other currency": {func(p *wallet.LedgerEntryParams) { p.BalanceAfter = usdAfter }, wallet.ErrInvalidLedgerEntry},
		"amount in other currency": {
			func(p *wallet.LedgerEntryParams) { p.Amount = usdAmount },
			money.ErrCurrencyMismatch,
		},
	}

	for name, tc := range cases {
		p := entryParams()
		tc.mutate(&p)
		e, err := wallet.NewLedgerEntry(p)
		assert.ErrorIs(t, err, tc.want, name)
		assert.ErrorIs(t, err, wallet.ErrInvalidLedgerEntry, name)
		assert.Equal(t, wallet.LedgerEntry{}, e, name)
	}
}

func TestApplyNeverMovesTheWalletBackInTime(t *testing.T) {
	t.Parallel()
	w := openWith(t, brl("100.00"))
	m := movement(wallet.Debit, brl("10.00"))
	m.Now = now.Add(-time.Hour)

	entry, err := w.Apply(m)
	require.NoError(t, err)
	assert.Equal(t, w.CreatedAt(), entry.CreatedAt(), "an earlier clock is clamped to the last change")
	assert.Equal(t, entry.CreatedAt(), w.UpdatedAt())

	_, err = wallet.Rehydrate(w.Snapshot())
	require.NoError(t, err)
}
