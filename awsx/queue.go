// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package awsx

import (
	"context"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
)

// Queue is the ear on the mailbox: SES writes the message, tells SNS, SNS puts
// it in this queue, and s3mail hangs on the queue with a long poll.
//
// Why not an S3 event notification on the bucket, which would be the obvious
// route: s3mail writes into that same bucket all the time - every read mark is
// an op object, and there are sent copies and drafts. A bucket notification
// fires for all of them, and the refresh it triggers writes again. Going
// through the SES receipt rule there is nothing to feed back: only incoming
// mail passes it.
type Queue struct {
	c   *sqs.Client
	url string
}

func NewQueue(cfg aws.Config, url string) *Queue {
	return &Queue{c: sqs.NewFromConfig(cfg), url: url}
}

// Wait hangs on the queue until something arrives or the wait is over, and
// returns how many messages came.
//
// Twenty seconds is what SQS allows at most, and long polling is what makes
// this cheap: one request per twenty seconds is about 130,000 a month, and the
// free tier is a million. Short polling would be the same in effect and dearer
// by a factor of twenty.
//
// What the message says does not matter. It only means: something arrived, go
// and look. The refresh that follows compares ETags and fetches what really
// changed - parsing the notification to save it that work would buy nothing and
// tie us to the shape of an SES notification.
func (q *Queue) Wait(ctx context.Context) (int, error) {
	out, err := q.c.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
		QueueUrl:            aws.String(q.url),
		MaxNumberOfMessages: 10,
		WaitTimeSeconds:     20,
		// Long enough to delete them below, short enough that a crash brings
		// them back soon rather than in a quarter of an hour.
		VisibilityTimeout: 30,
	})
	if err != nil {
		return 0, err
	}
	if len(out.Messages) == 0 {
		return 0, nil
	}

	entries := make([]types.DeleteMessageBatchRequestEntry, 0, len(out.Messages))
	for i, m := range out.Messages {
		entries = append(entries, types.DeleteMessageBatchRequestEntry{
			Id: aws.String(itoa(i)), ReceiptHandle: m.ReceiptHandle,
		})
	}
	// Deleted only after they have been taken. A message that stays because the
	// delete failed comes back and causes one refresh too many - which costs a
	// listing and changes nothing.
	if _, err := q.c.DeleteMessageBatch(ctx, &sqs.DeleteMessageBatchInput{
		QueueUrl: aws.String(q.url), Entries: entries,
	}); err != nil {
		return len(out.Messages), err
	}
	return len(out.Messages), nil
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// QueueURL turns the ARN from the IAM policy into the URL the SDK wants.
// arn:aws:sqs:eu-north-1:123456789012:s3mail-ole becomes
// https://sqs.eu-north-1.amazonaws.com/123456789012/s3mail-ole.
//
// Doing it here rather than asking GetQueueUrl saves a call and a permission:
// the ARN already carries everything, and a mailbox access that may receive
// need not also be allowed to look queues up.
func QueueURL(arn string) string {
	parts := strings.Split(arn, ":")
	if len(parts) != 6 || parts[0] != "arn" || parts[2] != "sqs" {
		return ""
	}
	region, account, name := parts[3], parts[4], parts[5]
	if region == "" || account == "" || name == "" || strings.ContainsAny(name, "*?") {
		return ""
	}
	return "https://sqs." + region + ".amazonaws.com/" + account + "/" + name
}
