package awssqs

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"

	"github.com/MurilojrMarques/backend-challenge-go/internal/application"
	"github.com/MurilojrMarques/backend-challenge-go/internal/config"
)

type Publisher struct {
	client *sqs.Client
	url    string
}

func NewPublisher(client *sqs.Client, cfg config.SQS) *Publisher {
	return &Publisher{client: client, url: cfg.EventsQueueURL}
}

func (p *Publisher) Publish(ctx context.Context, rec application.OutboxRecord) error {
	_, err := p.client.SendMessage(ctx, &sqs.SendMessageInput{
		QueueUrl:               aws.String(p.url),
		MessageBody:            aws.String(string(rec.Payload)),
		MessageGroupId:         aws.String(rec.AggregateID.String()),
		MessageDeduplicationId: aws.String(rec.EventID.String()),
		MessageAttributes: map[string]types.MessageAttributeValue{
			"eventType": {DataType: aws.String("String"), StringValue: aws.String(string(rec.EventType))},
			"eventId":   {DataType: aws.String("String"), StringValue: aws.String(rec.EventID.String())},
		},
	})
	return translate("publish", err)
}
