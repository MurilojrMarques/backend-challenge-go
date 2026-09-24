package postgres

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/fx"

	"github.com/MurilojrMarques/backend-challenge-go/internal/application"
	"github.com/MurilojrMarques/backend-challenge-go/internal/config"
)

var Module = fx.Module("postgres",
	fx.Provide(
		newPool,
		fx.Annotate(NewUnitOfWork, fx.As(new(application.UnitOfWork))),
		fx.Annotate(NewHealth, fx.As(new(application.HealthChecker)), fx.ResultTags(`group:"health"`)),
	),
)

func newPool(lc fx.Lifecycle, cfg config.Config) (*pgxpool.Pool, error) {
	pool, err := NewPool(context.Background(), cfg.Database.URL, cfg.Database.ConnectTimeout)
	if err != nil {
		return nil, err
	}
	lc.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			return Ping(ctx, pool)
		},
		OnStop: func(context.Context) error {
			pool.Close()
			return nil
		},
	})
	return pool, nil
}
