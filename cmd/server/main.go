package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"go.uber.org/fx"
	"go.uber.org/fx/fxevent"

	"github.com/MurilojrMarques/backend-challenge-go/internal/bootstrap"
)

const (
	defaultStartTimeout = 30 * time.Second
	defaultStopTimeout  = 25 * time.Second
)

var shutdownSignals = []os.Signal{os.Interrupt, syscall.SIGTERM}

func main() {
	os.Exit(run())
}

func run() int {
	level, err := logLevelEnv("LOG_LEVEL", slog.LevelInfo)
	if err != nil {
		slog.Error("invalid LOG_LEVEL", "err", err)
		return 2
	}
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
	slog.SetDefault(logger)

	roles, err := bootstrap.ParseRoles(os.Getenv("APP_ROLES"))
	if err != nil {
		logger.Error("invalid APP_ROLES", "err", err)
		return 2
	}
	logger = logger.With("roles", roles.Strings())

	startTimeout, err := durationEnv("APP_START_TIMEOUT", defaultStartTimeout)
	if err != nil {
		logger.Error("invalid APP_START_TIMEOUT", "err", err)
		return 2
	}
	stopTimeout, err := durationEnv("APP_STOP_TIMEOUT", defaultStopTimeout)
	if err != nil {
		logger.Error("invalid APP_STOP_TIMEOUT", "err", err)
		return 2
	}

	app := fx.New(
		bootstrap.New(roles),
		fx.WithLogger(func() fxevent.Logger {
			l := &fxevent.SlogLogger{Logger: logger.With("component", "fx")}
			l.UseLogLevel(slog.LevelDebug)
			return l
		}),
	)

	startCtx, stopStartSignals := signal.NotifyContext(context.Background(), shutdownSignals...)
	startCtx, cancelStart := context.WithTimeout(startCtx, startTimeout)
	err = app.Start(startCtx)
	done := app.Wait()
	cancelStart()
	stopStartSignals()
	if err != nil {
		switch {
		case errors.Is(err, context.Canceled):
			logger.Warn("startup interrupted by signal")
		case errors.Is(err, context.DeadlineExceeded):
			logger.Error("startup exceeded deadline", "timeout", startTimeout)
		default:
			logger.Error("startup failed", "err", err)
		}
		return 1
	}
	logger.Info("application started")

	sig := <-done
	logger.Info("shutdown signal received", "signal", sig.Signal.String())

	stopCtx, stopStopSignals := signal.NotifyContext(context.Background(), shutdownSignals...)
	defer stopStopSignals()
	stopCtx, cancelStop := context.WithTimeout(stopCtx, stopTimeout)
	defer cancelStop()
	if err := app.Stop(stopCtx); err != nil {
		switch {
		case errors.Is(err, context.Canceled):
			logger.Error("shutdown aborted by second signal")
		case errors.Is(err, context.DeadlineExceeded):
			logger.Error("shutdown exceeded deadline", "timeout", stopTimeout)
		default:
			logger.Error("shutdown failed", "err", err)
		}
		return 1
	}
	logger.Info("application stopped")
	return sig.ExitCode
}

func durationEnv(name string, def time.Duration) (time.Duration, error) {
	raw, ok := os.LookupEnv(name)
	if !ok || raw == "" {
		return def, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, err
	}
	if d <= 0 {
		return 0, errors.New("must be positive")
	}
	return d, nil
}

func logLevelEnv(name string, def slog.Level) (slog.Level, error) {
	raw, ok := os.LookupEnv(name)
	if !ok || raw == "" {
		return def, nil
	}
	var level slog.Level
	if err := level.UnmarshalText([]byte(raw)); err != nil {
		return 0, err
	}
	return level, nil
}
