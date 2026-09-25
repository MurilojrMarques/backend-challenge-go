package worker

import (
	"log/slog"
	"os"
)

const (
	FaultConsumerAfterCommit = "consumer.after_commit_before_ack"
	FaultOutboxAfterPublish  = "outbox.after_publish_before_mark"
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
	f.logger.Error("fault injected, terminating process", "point", point)
	f.exit(1)
}
