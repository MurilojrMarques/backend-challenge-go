package awssqs

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"go.uber.org/fx"

	"github.com/MurilojrMarques/backend-challenge-go/internal/application"
	"github.com/MurilojrMarques/backend-challenge-go/internal/config"
)

var Module = fx.Module("awssqs",
	fx.Provide(
		func(cfg config.Config) (*sqs.Client, error) {
			return NewClient(context.Background(), cfg.AWS)
		},
		fx.Annotate(
			func(client *sqs.Client, cfg config.Config) *Queue { return NewQueue(client, cfg.SQS) },
			fx.As(new(application.Queue)),
		),
		fx.Annotate(
			func(client *sqs.Client, cfg config.Config) *Publisher { return NewPublisher(client, cfg.SQS) },
			fx.As(new(application.EventPublisher)),
		),
		fx.Annotate(
			func(client *sqs.Client, cfg config.Config) *Health { return NewHealth(client, cfg.SQS.WagerQueueURL) },
			fx.As(new(application.HealthChecker)),
			fx.ResultTags(`group:"health"`),
		),
	),
)
