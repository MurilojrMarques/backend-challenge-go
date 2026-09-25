package awssqs

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
)

type Health struct {
	client *sqs.Client
	url    string
}

func NewHealth(client *sqs.Client, queueURL string) *Health {
	return &Health{client: client, url: queueURL}
}

func (h *Health) Name() string {
	return "sqs"
}

func (h *Health) Check(ctx context.Context) error {
	_, err := h.client.GetQueueAttributes(ctx, &sqs.GetQueueAttributesInput{
		QueueUrl:       aws.String(h.url),
		AttributeNames: []types.QueueAttributeName{types.QueueAttributeNameQueueArn},
	})
	return translate("health", err)
}
