package telemetry

import (
	"log/slog"
	"net/http"

	"go.uber.org/fx"

	"github.com/MurilojrMarques/backend-challenge-go/internal/application"
)

var Module = fx.Module("telemetry",
	fx.Provide(
		slog.Default,
		func() application.Clock { return application.SystemClock{} },
		NewMetrics,
		func(m *Metrics) application.Metrics { return m },
		fx.Annotate(
			func(m *Metrics) http.Handler { return m.Handler() },
			fx.ResultTags(`name:"metrics"`),
		),
	),
)
