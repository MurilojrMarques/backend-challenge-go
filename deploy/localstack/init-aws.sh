#!/bin/bash
set -euo pipefail

export AWS_DEFAULT_REGION="${AWS_DEFAULT_REGION:-us-east-1}"

create_fifo_queue() {
  local name="$1"
  local attributes="$2"
  awslocal sqs create-queue --queue-name "$name" --attributes "$attributes" >/dev/null
  echo "queue ready: $name"
}

queue_arn() {
  local url
  url=$(awslocal sqs get-queue-url --queue-name "$1" --query QueueUrl --output text)
  awslocal sqs get-queue-attributes --queue-url "$url" --attribute-names QueueArn --query Attributes.QueueArn --output text
}

create_fifo_queue wager-transactions-dlq.fifo '{
  "FifoQueue": "true",
  "ContentBasedDeduplication": "false",
  "MessageRetentionPeriod": "1209600"
}'

DLQ_ARN=$(queue_arn wager-transactions-dlq.fifo)

create_fifo_queue wager-transactions.fifo "{
  \"FifoQueue\": \"true\",
  \"ContentBasedDeduplication\": \"false\",
  \"VisibilityTimeout\": \"30\",
  \"ReceiveMessageWaitTimeSeconds\": \"20\",
  \"RedrivePolicy\": \"{\\\"deadLetterTargetArn\\\":\\\"${DLQ_ARN}\\\",\\\"maxReceiveCount\\\":\\\"5\\\"}\"
}"

create_fifo_queue wallet-events.fifo '{
  "FifoQueue": "true",
  "ContentBasedDeduplication": "false",
  "MessageRetentionPeriod": "345600"
}'

WAGER_ARN=$(queue_arn wager-transactions.fifo)
EVENTS_ARN=$(queue_arn wallet-events.fifo)

create_user_with_policy() {
  local user="$1"
  local policy_name="$2"
  local document="$3"
  awslocal iam create-user --user-name "$user" >/dev/null 2>&1 || true
  awslocal iam put-user-policy --user-name "$user" --policy-name "$policy_name" --policy-document "$document"
  awslocal iam create-access-key --user-name "$user" --query 'AccessKey.[AccessKeyId,SecretAccessKey]' --output text \
    | awk -v u="$user" '{ printf "iam user %s: access key %s\n", u, $1 }'
}

create_user_with_policy wallet-consumer wager-consumer "{
  \"Version\": \"2012-10-17\",
  \"Statement\": [
    { \"Effect\": \"Allow\",
      \"Action\": [\"sqs:ReceiveMessage\", \"sqs:DeleteMessage\", \"sqs:ChangeMessageVisibility\", \"sqs:GetQueueAttributes\"],
      \"Resource\": \"${WAGER_ARN}\" },
    { \"Effect\": \"Allow\", \"Action\": [\"sqs:SendMessage\"], \"Resource\": \"${DLQ_ARN}\" }
  ]
}"

create_user_with_policy wallet-publisher wallet-events-publisher "{
  \"Version\": \"2012-10-17\",
  \"Statement\": [
    { \"Effect\": \"Allow\", \"Action\": [\"sqs:SendMessage\", \"sqs:GetQueueAttributes\"], \"Resource\": \"${EVENTS_ARN}\" }
  ]
}"

create_user_with_policy provider-gateway wager-producer "{
  \"Version\": \"2012-10-17\",
  \"Statement\": [
    { \"Effect\": \"Allow\", \"Action\": [\"sqs:SendMessage\"], \"Resource\": \"${WAGER_ARN}\" }
  ]
}"

echo "localstack init complete"
