package bootstrap

import (
	"log/slog"

	"go.uber.org/fx"

	"github.com/MurilojrMarques/backend-challenge-go/internal/adapters/awssqs"
	"github.com/MurilojrMarques/backend-challenge-go/internal/adapters/httpapi"
	"github.com/MurilojrMarques/backend-challenge-go/internal/adapters/postgres"
	"github.com/MurilojrMarques/backend-challenge-go/internal/adapters/telemetry"
	"github.com/MurilojrMarques/backend-challenge-go/internal/application"
	"github.com/MurilojrMarques/backend-challenge-go/internal/application/wagering"
	"github.com/MurilojrMarques/backend-challenge-go/internal/application/wallets"
	"github.com/MurilojrMarques/backend-challenge-go/internal/config"
	"github.com/MurilojrMarques/backend-challenge-go/internal/domain/wager"
	"github.com/MurilojrMarques/backend-challenge-go/internal/worker"
)

func New(roles Roles) fx.Option {
	opts := []fx.Option{
		fx.Supply(roles),
		config.Module,
		telemetry.Module,
		postgres.Module,
		applicationModule,
		fx.Provide(httpOptions, requestObserver),
		httpapi.Module,
	}
	if roles.Any(RoleConsumer, RoleOutbox) {
		opts = append(opts, awssqs.Module)
	}
	if roles.Any(RoleConsumer, RoleOutbox, RolePending) {
		opts = append(opts, worker.Module)
	}
	if roles.Has(RoleConsumer) {
		opts = append(opts, worker.ConsumerModule)
	}
	if roles.Has(RoleOutbox) {
		opts = append(opts, worker.OutboxModule)
	}
	if roles.Has(RolePending) {
		opts = append(opts, worker.PendingModule)
	}
	return fx.Options(opts...)
}

var applicationModule = fx.Module("application",
	fx.Provide(wallets.NewService, newWageringService),
)

func newWageringService(uow application.UnitOfWork, clock application.Clock, metrics application.Metrics, logger *slog.Logger, cfg config.Config) *wagering.Service {
	return wagering.NewService(uow, clock, metrics, logger, wagering.Options{
		ReferencePolicy:  wager.ReferencePolicy{MaxAttempts: cfg.Pending.MaxAttempts, TTL: cfg.Pending.TTL},
		ReferenceBackoff: wagering.Backoff{Base: cfg.Pending.BackoffBase, Max: cfg.Pending.BackoffMax},
	})
}

func httpOptions(roles Roles, cfg config.Config) httpapi.Options {
	return httpapi.Options{EnableAPI: roles.Has(RoleAPI), MaxBodyBytes: cfg.HTTP.MaxBodyBytes}
}

func requestObserver(m *telemetry.Metrics) httpapi.RequestObserver {
	return m
}
