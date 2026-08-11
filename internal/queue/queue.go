// Package queue は SQS FIFO Queue へのジョブ投入 adapter を提供する（SPECIFICATION.md 7）。
//
// Enqueuer は dispatch ユースケースの EnqueueBatch と各 worker ユースケースの Enqueue の
// 両方を満たす。Amazon 系ジョブは MessageGroupId=amazon-requests、gist_update は external-updates とする。
// MessageDeduplicationId には job_id の SHA-256 lowercase hex を使う（SPECIFICATION.md 7.2、AGENTS.md 4）。
package queue

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"strconv"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/shinderuman/kindle-automation/internal/job"
)

// SQSAPI は SQS SendMessageBatch の必要部分だけを取り出した interface。*sqs.Client が満たす。
type SQSAPI interface {
	SendMessageBatch(ctx context.Context, in *sqs.SendMessageBatchInput, opts ...func(*sqs.Options)) (*sqs.SendMessageBatchOutput, error)
}

type Enqueuer struct {
	api      SQSAPI
	queueURL string
	logger   *slog.Logger
}

func NewEnqueuer(api SQSAPI, queueURL string, logger *slog.Logger) *Enqueuer {
	return &Enqueuer{api: api, queueURL: queueURL, logger: logger}
}

// Enqueue は各 worker ユースケースの Enqueuer interface を満たす。
func (e *Enqueuer) Enqueue(ctx context.Context, j job.Job) error {
	return e.sendBatch(ctx, []job.Job{j})
}

// EnqueueBatch は10件単位で順番に送信する。dispatch ユースケースの Enqueuer interface を満たす。
// batch 内の失敗 entry を見落とさず error とする（SPECIFICATION.md 7.3、AGENTS.md 4）。
func (e *Enqueuer) EnqueueBatch(ctx context.Context, jobs []job.Job) error {
	for i := 0; i < len(jobs); i += 10 {
		end := i + 10
		if end > len(jobs) {
			end = len(jobs)
		}
		if err := e.sendBatch(ctx, jobs[i:end]); err != nil {
			return err
		}
	}
	return nil
}

func (e *Enqueuer) sendBatch(ctx context.Context, jobs []job.Job) error {
	entries := make([]sqstypes.SendMessageBatchRequestEntry, len(jobs))
	for i, j := range jobs {
		body, err := j.Encode()
		if err != nil {
			return fmt.Errorf("encode job %s: %w", j.JobID, err)
		}
		entries[i] = sqstypes.SendMessageBatchRequestEntry{
			Id:                     aws.String(strconv.Itoa(i)),
			MessageBody:            aws.String(string(body)),
			MessageGroupId:         aws.String(job.MessageGroup(j.Kind)),
			MessageDeduplicationId: aws.String(DeduplicationID(j.JobID)),
		}
	}
	out, err := e.api.SendMessageBatch(ctx, &sqs.SendMessageBatchInput{
		QueueUrl: aws.String(e.queueURL),
		Entries:  entries,
	})
	if err != nil {
		return fmt.Errorf("send message batch: %w", err)
	}
	if len(out.Failed) > 0 {
		for _, f := range out.Failed {
			if e.logger != nil {
				e.logger.ErrorContext(ctx, "sqs batch entry failed",
					slog.String("entry_id", aws.ToString(f.Id)),
					slog.String("code", aws.ToString(f.Code)),
					slog.String("message", aws.ToString(f.Message)),
				)
			}
		}
		first := out.Failed[0]
		return fmt.Errorf("send message batch: entry %s failed: %s", aws.ToString(first.Id), aws.ToString(first.Message))
	}
	return nil
}

// DeduplicationID は job_id の SHA-256 lowercase hex を返す（SPECIFICATION.md 7.2）。
func DeduplicationID(jobID string) string {
	sum := sha256.Sum256([]byte(jobID))
	return hex.EncodeToString(sum[:])
}
