package testutil

import (
	"context"
	"encoding/json"
	"strconv"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
)

const (
	Region      = "us-east-1"
	AccountID   = "000000000000"
	WagerQueue  = "wager-transactions.fifo"
	WagerDLQ    = "wager-transactions-dlq.fifo"
	EventsQueue = "wallet-events.fifo"
)

func NewSQS(endpoint string) *sqs.Client {
	return sqs.New(sqs.Options{
		Region:       Region,
		BaseEndpoint: aws.String(endpoint),
		Credentials:  credentials.NewStaticCredentialsProvider("test", "test", ""),
	})
}

func QueueURL(endpoint, name string) string {
	return endpoint + "/" + AccountID + "/" + name
}

func Send(ctx context.Context, client *sqs.Client, queueURL, groupID, dedupID string, body any) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	_, err = client.SendMessage(ctx, &sqs.SendMessageInput{
		QueueUrl:               aws.String(queueURL),
		MessageBody:            aws.String(string(raw)),
		MessageGroupId:         aws.String(groupID),
		MessageDeduplicationId: aws.String(dedupID),
	})
	return err
}

func Receive(ctx context.Context, client *sqs.Client, queueURL string, waitSeconds int32) ([]types.Message, error) {
	out, err := client.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
		QueueUrl:              aws.String(queueURL),
		MaxNumberOfMessages:   10,
		WaitTimeSeconds:       waitSeconds,
		MessageAttributeNames: []string{"All"},
		MessageSystemAttributeNames: []types.MessageSystemAttributeName{
			types.MessageSystemAttributeNameMessageGroupId,
			types.MessageSystemAttributeNameApproximateReceiveCount,
		},
	})
	if err != nil {
		return nil, err
	}
	return out.Messages, nil
}

func Delete(ctx context.Context, client *sqs.Client, queueURL, receiptHandle string) error {
	_, err := client.DeleteMessage(ctx, &sqs.DeleteMessageInput{QueueUrl: aws.String(queueURL), ReceiptHandle: aws.String(receiptHandle)})
	return err
}

func Depth(ctx context.Context, client *sqs.Client, queueURL string) (int, error) {
	out, err := client.GetQueueAttributes(ctx, &sqs.GetQueueAttributesInput{
		QueueUrl: aws.String(queueURL),
		AttributeNames: []types.QueueAttributeName{
			types.QueueAttributeNameApproximateNumberOfMessages,
			types.QueueAttributeNameApproximateNumberOfMessagesNotVisible,
		},
	})
	if err != nil {
		return 0, err
	}
	total := 0
	for _, v := range out.Attributes {
		n, _ := strconv.Atoi(v)
		total += n
	}
	return total, nil
}

func Drain(ctx context.Context, client *sqs.Client, queueURL string, idle time.Duration) ([]types.Message, error) {
	var all []types.Message
	quiet := time.Now()
	for time.Since(quiet) < idle {
		msgs, err := Receive(ctx, client, queueURL, 1)
		if err != nil {
			return all, err
		}
		for _, m := range msgs {
			if err := Delete(ctx, client, queueURL, aws.ToString(m.ReceiptHandle)); err != nil {
				return all, err
			}
		}
		if len(msgs) > 0 {
			quiet = time.Now()
		}
		all = append(all, msgs...)
	}
	return all, nil
}

func Payload(m types.Message) map[string]any {
	var out map[string]any
	_ = json.Unmarshal([]byte(aws.ToString(m.Body)), &out)
	return out
}
