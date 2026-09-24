package bootstrap

import (
	"log/slog"

	"go.uber.org/fx"
)

func New(roles Roles) fx.Option {
	return fx.Options(
		fx.Supply(roles),
		fx.Invoke(logRoles),
	)
}

func logRoles(roles Roles) {
	slog.Info("roles resolved", "roles", roles.Strings())
}
