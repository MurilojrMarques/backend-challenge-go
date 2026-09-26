package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"go.uber.org/fx"

	"github.com/MurilojrMarques/backend-challenge-go/internal/application"
	"github.com/MurilojrMarques/backend-challenge-go/internal/application/wagering"
	"github.com/MurilojrMarques/backend-challenge-go/internal/application/wallets"
	"github.com/MurilojrMarques/backend-challenge-go/internal/config"
)

type Params struct {
	fx.In

	Config   config.Config
	Options  Options
	Logger   *slog.Logger
	Verifier TokenVerifier
	Wallets  *wallets.Service            `optional:"true"`
	Wagers   *wagering.Service           `optional:"true"`
	Health   []application.HealthChecker `group:"health"`
	Metrics  http.Handler                `name:"metrics" optional:"true"`
	Observer RequestObserver             `optional:"true"`
}

var Module = fx.Module("httpapi",
	fx.Provide(
		newVerifier,
		newRouter,
		newServer,
	),
	fx.Invoke(func(*Server) {}),
)

func newVerifier(lc fx.Lifecycle, cfg config.Config, opts Options) TokenVerifier {
	v := NewOIDCVerifier(cfg.OIDC)
	if opts.EnableAPI {
		lc.Append(fx.Hook{OnStart: v.Start})
	}
	return v
}

func newRouter(p Params) (http.Handler, error) {
	opts := p.Options
	if opts.MaxBodyBytes <= 0 {
		opts.MaxBodyBytes = p.Config.HTTP.MaxBodyBytes
	}
	if opts.RequestTimeout <= 0 && p.Config.HTTP.WriteTimeout > time.Second {
		opts.RequestTimeout = p.Config.HTTP.WriteTimeout - time.Second
	}
	return NewRouter(Deps{
		Options:  opts,
		Logger:   p.Logger,
		Verifier: p.Verifier,
		Wallets:  p.Wallets,
		Wagers:   p.Wagers,
		Health:   p.Health,
		Metrics:  p.Metrics,
		Observer: p.Observer,
	})
}

func newServer(lc fx.Lifecycle, cfg config.Config, handler http.Handler, logger *slog.Logger) *Server {
	s := NewServer(cfg.HTTP, handler, logger)
	lc.Append(fx.Hook{
		OnStart: s.Start,
		OnStop: func(ctx context.Context) error {
			return s.Stop(ctx)
		},
	})
	return s
}
