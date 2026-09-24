package main

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"github.com/golang-migrate/migrate/v4/source/iofs"

	"github.com/MurilojrMarques/backend-challenge-go/migrations"
)

const usage = "usage: migrate up | down [N|all] | version | force N"

func main() {
	os.Exit(run(os.Args[1:], os.Getenv("DATABASE_URL")))
}

func run(args []string, databaseURL string) int {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))

	if len(args) == 0 {
		logger.Error(usage)
		return 2
	}
	if databaseURL == "" {
		logger.Error("DATABASE_URL is required")
		return 2
	}

	m, err := newMigrator(databaseURL)
	if err != nil {
		logger.Error("cannot initialise migrator", "err", err)
		return 1
	}
	defer func() {
		if srcErr, dbErr := m.Close(); srcErr != nil || dbErr != nil {
			logger.Warn("close", "sourceErr", srcErr, "databaseErr", dbErr)
		}
	}()

	if err := execute(m, args, logger); err != nil {
		if errors.Is(err, errUsage) {
			logger.Error(err.Error())
			return 2
		}
		logger.Error("migration failed", "command", args[0], "err", err)
		return 1
	}
	return 0
}

var errUsage = errors.New(usage)

func execute(m *migrate.Migrate, args []string, logger *slog.Logger) error {
	switch args[0] {
	case "up":
		return report(logger, "up", m.Up(), m)
	case "down":
		steps, err := downSteps(args[1:])
		if err != nil {
			return err
		}
		if steps == 0 {
			return report(logger, "down all", m.Down(), m)
		}
		return report(logger, fmt.Sprintf("down %d", steps), m.Steps(-steps), m)
	case "version":
		return report(logger, "version", nil, m)
	case "force":
		if len(args) != 2 {
			return errUsage
		}
		version, err := strconv.Atoi(args[1])
		if err != nil {
			return fmt.Errorf("%w: invalid version %q", errUsage, args[1])
		}
		return report(logger, "force", m.Force(version), m)
	default:
		return fmt.Errorf("%w: unknown command %q", errUsage, args[0])
	}
}

func downSteps(args []string) (int, error) {
	if len(args) == 0 {
		return 1, nil
	}
	if args[0] == "all" {
		return 0, nil
	}
	steps, err := strconv.Atoi(args[0])
	if err != nil || steps < 1 {
		return 0, fmt.Errorf("%w: invalid step count %q", errUsage, args[0])
	}
	return steps, nil
}

func report(logger *slog.Logger, command string, err error, m *migrate.Migrate) error {
	if err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return err
	}
	version, dirty, verr := m.Version()
	if errors.Is(verr, migrate.ErrNilVersion) {
		logger.Info("migrations applied", "command", command, "version", 0, "dirty", false, "noChange", errors.Is(err, migrate.ErrNoChange))
		return nil
	}
	if verr != nil {
		return verr
	}
	logger.Info("migrations applied", "command", command, "version", version, "dirty", dirty, "noChange", errors.Is(err, migrate.ErrNoChange))
	if dirty {
		return fmt.Errorf("schema is dirty at version %d; fix it and run force %d", version, version)
	}
	return nil
}

func newMigrator(databaseURL string) (*migrate.Migrate, error) {
	source, err := iofs.New(migrations.FS, ".")
	if err != nil {
		return nil, err
	}
	return migrate.NewWithSourceInstance("iofs", source, toPgxURL(databaseURL))
}

func toPgxURL(databaseURL string) string {
	for _, scheme := range []string{"postgresql://", "postgres://"} {
		if strings.HasPrefix(databaseURL, scheme) {
			return "pgx5://" + strings.TrimPrefix(databaseURL, scheme)
		}
	}
	return databaseURL
}
