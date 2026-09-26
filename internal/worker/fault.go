package worker

import (
	"log/slog"
	"os"

	"github.com/MurilojrMarques/backend-challenge-go/internal/config"
)

const (
	FaultConsumerAfterCommit = config.FaultConsumerAfterCommit
	FaultOutboxAfterPublish  = config.FaultOutboxAfterPublish
	faultExitCode            = 3
)

type Fault struct {
	point  string
	logger *slog.Logger
	exit   func(code int)
}

func NewFault(point string, logger *slog.Logger) *Fault {
	return &Fault{point: point, logger: logger, exit: os.Exit}
}

func (f *Fault) Trigger(point string) {
	if f == nil || f.point == "" || f.point != point {
		return
	}
	f.logger.Error("fault injected, terminating process", "point", point, "exitCode", faultExitCode)
	f.exit(faultExitCode)
}
