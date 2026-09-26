//go:build integration

package integration

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/MurilojrMarques/backend-challenge-go/internal/adapters/postgres"
	"github.com/MurilojrMarques/backend-challenge-go/internal/application"
	"github.com/MurilojrMarques/backend-challenge-go/internal/application/apptest"
	"github.com/MurilojrMarques/backend-challenge-go/internal/application/wagering"
	"github.com/MurilojrMarques/backend-challenge-go/internal/application/wallets"
	"github.com/MurilojrMarques/backend-challenge-go/internal/domain/money"
	"github.com/MurilojrMarques/backend-challenge-go/test/testutil"
)

var (
	pg *testutil.Postgres
	kc *testutil.Keycloak
	ls *testutil.LocalStack

	discard  = slog.New(slog.NewTextHandler(io.Discard, nil))
	internal = application.Principal{Subject: "integration", Role: application.RoleInternal}
	provider = application.Principal{Subject: "provider-a", Role: application.RoleProvider, ProviderID: testutil.ProviderAClient}
)

func TestMain(m *testing.M) {
	os.Setenv("AWS_ACCESS_KEY_ID", "test")
	os.Setenv("AWS_SECRET_ACCESS_KEY", "test")

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()

	errs := make(chan error, 3)
	var wg sync.WaitGroup
	start := func(name string, fn func() error) {
		defer wg.Done()
		defer func() {
			if rec := recover(); rec != nil {
				errs <- fmt.Errorf("%s: %v", name, rec)
			}
		}()
		if err := fn(); err != nil {
			errs <- fmt.Errorf("%s: %w", name, err)
		}
	}
	wg.Add(3)
	go start("postgres", func() (err error) { pg, err = testutil.StartPostgres(ctx); return err })
	go start("keycloak", func() (err error) { kc, err = testutil.StartKeycloak(ctx); return err })
	go start("localstack", func() (err error) { ls, err = testutil.StartLocalStack(ctx); return err })
	wg.Wait()
	close(errs)

	failed := false
	for err := range errs {
		if err != nil {
			failed = true
			fmt.Fprintln(os.Stderr, "integration:", err)
		}
	}
	code := 1
	if !failed {
		code = m.Run()
	}
	terminate()
	os.Exit(code)
}

func terminate() {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if pg != nil {
		_ = pg.Terminate(ctx)
	}
	if kc != nil {
		_ = kc.Terminate(ctx)
	}
	if ls != nil {
		_ = ls.Terminate(ctx)
	}
}

type services struct {
	pool    *pgxpool.Pool
	uow     *postgres.UnitOfWork
	wallets *wallets.Service
	wagers  *wagering.Service
}

func newServices(t *testing.T) services {
	t.Helper()
	pool, err := postgres.NewPool(context.Background(), pg.AppURL(), 10*time.Second)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	uow := postgres.NewUnitOfWork(pool)
	clock := application.SystemClock{}
	return services{
		pool:    pool,
		uow:     uow,
		wallets: wallets.NewService(uow, clock, application.NopMetrics{}, discard),
		wagers:  wagering.NewService(uow, clock, application.NopMetrics{}, discard, apptest.DefaultOptions),
	}
}

func rawPool(t *testing.T, url string) *pgxpool.Pool {
	t.Helper()
	pool, err := pgxpool.New(context.Background(), url)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	return pool
}

func newID() uuid.UUID {
	return uuid.Must(uuid.NewV7())
}

func brl(amount string) money.Money {
	return money.MustParse(amount, money.BRL)
}

func (s services) open(t *testing.T, amount string) wallets.View {
	t.Helper()
	view, err := s.wallets.Open(context.Background(), internal, wallets.OpenCommand{
		PlayerID:       newID().String(),
		InitialBalance: application.MoneyInput{Amount: amount, Currency: "BRL"},
	})
	require.NoError(t, err)
	return view
}

func command(w wallets.View, kind, externalID, amount string) wagering.Command {
	return wagering.Command{
		IdempotencyKey:        testutil.ProviderAClient + ":" + externalID,
		ProviderID:            testutil.ProviderAClient,
		ExternalTransactionID: externalID,
		PlayerID:              w.PlayerID.String(),
		WalletID:              w.ID.String(),
		RoundID:               "round-1",
		GameID:                "fortune-chimp",
		Kind:                  kind,
		Money:                 application.MoneyInput{Amount: amount, Currency: "BRL"},
	}
}

func (s services) balance(t *testing.T, id uuid.UUID) money.Money {
	t.Helper()
	view, err := s.wallets.Get(context.Background(), internal, id)
	require.NoError(t, err)
	return view.Balance
}
