package awssqs

import (
	"context"
	"strconv"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"

	"github.com/MurilojrMarques/backend-challenge-go/internal/application"
	"github.com/MurilojrMarques/backend-challenge-go/internal/config"
)

const maxVisibilityTimeout = 12 * time.Hour

type Queue struct {
	client      *sqs.Client
	url         string
	dlqURL      string
	waitTime    time.Duration
	visibility  time.Duration
	maxMessages int32
}

func NewQueue(client *sqs.Client, cfg config.SQS) *Queue {
	return &Queue{
		client:      client,
		url:         cfg.WagerQueueURL,
		dlqURL:      cfg.WagerDLQURL,
		waitTime:    cfg.WaitTime,
		visibility:  cfg.VisibilityTimeout,
		maxMessages: int32(cfg.MaxMessages),
	}
}

func (q *Queue) Receive(ctx context.Context) ([]application.Message, error) {
	out, err := q.client.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
		QueueUrl:            aws.String(q.url),
		MaxNumberOfMessages: q.maxMessages,
		WaitTimeSeconds:     int32(q.waitTime / time.Second),
		VisibilityTimeout:   int32(q.visibility / time.Second),
		MessageSystemAttributeNames: []types.MessageSystemAttributeName{
			types.MessageSystemAttributeNameApproximateReceiveCount,
			types.MessageSystemAttributeNameMessageGroupId,
		},
	})
	if err != nil {
		return nil, translate("receive", err)
	}
	msgs := make([]application.Message, 0, len(out.Messages))
	for _, m := range out.Messages {
		count, _ := strconv.Atoi(m.Attributes[string(types.MessageSystemAttributeNameApproximateReceiveCount)])
		msgs = append(msgs, application.Message{
			ID:            aws.ToString(m.MessageId),
			GroupID:       m.Attributes[string(types.MessageSystemAttributeNameMessageGroupId)],
			ReceiptHandle: aws.ToString(m.ReceiptHandle),
			Body:          []byte(aws.ToString(m.Body)),
			ReceiveCount:  count,
		})
	}
	return msgs, nil
}

func (q *Queue) Delete(ctx context.Context, receiptHandle string) error {
	_, err := q.client.DeleteMessage(ctx, &sqs.DeleteMessageInput{
		QueueUrl:      aws.String(q.url),
		ReceiptHandle: aws.String(receiptHandle),
	})
	return translate("delete", err)
}

func (q *Queue) ChangeVisibility(ctx context.Context, receiptHandle string, timeout time.Duration) error {
	if timeout > maxVisibilityTimeout {
		timeout = maxVisibilityTimeout
	}
	if timeout < 0 {
		timeout = 0
	}
	_, err := q.client.ChangeMessageVisibility(ctx, &sqs.ChangeMessageVisibilityInput{
		QueueUrl:          aws.String(q.url),
		ReceiptHandle:     aws.String(receiptHandle),
		VisibilityTimeout: int32(timeout / time.Second),
	})
	return translate("change-visibility", err)
}

func (q *Queue) SendToDeadLetter(ctx context.Context, msg application.Message, reason string) error {
	_, err := q.client.SendMessage(ctx, &sqs.SendMessageInput{
		QueueUrl:               aws.String(q.dlqURL),
		MessageBody:            aws.String(string(msg.Body)),
		MessageGroupId:         aws.String("dead-letter"),
		MessageDeduplicationId: aws.String(msg.ID),
		MessageAttributes: map[string]types.MessageAttributeValue{
			"reason":            {DataType: aws.String("String"), StringValue: aws.String(reason)},
			"originalMessageId": {DataType: aws.String("String"), StringValue: aws.String(msg.ID)},
			"receiveCount":      {DataType: aws.String("Number"), StringValue: aws.String(strconv.Itoa(msg.ReceiveCount))},
		},
	})
	return translate("send-to-dlq", err)
}
