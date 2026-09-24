package config

import (
	"os"

	"go.uber.org/fx"
)

var Module = fx.Module("config",
	fx.Provide(func() (Config, error) {
		return Load(os.Getenv)
	}),
)
