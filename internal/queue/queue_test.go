package queue

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/shinderuman/kindle-automation/internal/job"
)

type stubSQS struct {
	batches  [][]sqstypes.SendMessageBatchRequestEntry
	failIDs  map[string]string // entry Id -> error message
	failErr  error             // SendMessageBatch 自体のエラー
	queueURL string
}

func (s *stubSQS) SendMessageBatch(_ context.Context, in *sqs.SendMessageBatchInput, _ ...func(*sqs.Options)) (*sqs.SendMessageBatchOutput, error) {
	s.queueURL = aws.ToString(in.QueueUrl)
	s.batches = append(s.batches, in.Entries)
	if s.failErr != nil {
		return nil, s.failErr
	}
	var failed []sqstypes.BatchResultErrorEntry
	for _, e := range in.Entries {
		if msg, ok := s.failIDs[aws.ToString(e.Id)]; ok {
			failed = append(failed, sqstypes.BatchResultErrorEntry{Id: e.Id, Code: aws.String("InternalError"), Message: aws.String(msg)})
		}
	}
	return &sqs.SendMessageBatchOutput{Failed: failed}, nil
}

func sampleJob(id string, kind job.Kind) job.Job {
	return job.Job{Version: job.Version, JobID: id, Kind: kind, CheckType: job.CheckSale, CycleID: "sale:c", Target: job.Target{ASIN: "B000000000"}}
}

func TestEnqueue_SetsGroupAndDedup(t *testing.T) {
	s := &stubSQS{}
	enq := NewEnqueuer(s, "queue-url", nil)
	j := sampleJob("sale:c:B0AMAZON001", job.KindSaleCheck)

	if err := enq.Enqueue(context.Background(), j); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if s.queueURL != "queue-url" {
		t.Errorf("queueURL = %q", s.queueURL)
	}
	entry := s.batches[0][0]
	if aws.ToString(entry.MessageGroupId) != "amazon-requests" {
		t.Errorf("MessageGroupId = %q, want amazon-requests", aws.ToString(entry.MessageGroupId))
	}
	want := sha256sum("sale:c:B0AMAZON001")
	if aws.ToString(entry.MessageDeduplicationId) != want {
		t.Errorf("DeduplicationId = %q, want %q", aws.ToString(entry.MessageDeduplicationId), want)
	}
}

func TestEnqueue_GistUsesExternalUpdatesGroup(t *testing.T) {
	s := &stubSQS{}
	enq := NewEnqueuer(s, "queue-url", nil)
	j := job.Job{Version: job.Version, JobID: "sale:c:sale", Kind: job.KindGistUpdate, CheckType: job.CheckSale, Target: job.Target{GistType: "sale"}}

	if err := enq.Enqueue(context.Background(), j); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if aws.ToString(s.batches[0][0].MessageGroupId) != "external-updates" {
		t.Errorf("gist MessageGroupId = %q, want external-updates", aws.ToString(s.batches[0][0].MessageGroupId))
	}
}

func TestEnqueueBatch_ChunksInTens(t *testing.T) {
	s := &stubSQS{}
	enq := NewEnqueuer(s, "queue-url", nil)
	jobs := make([]job.Job, 12)
	for i := range jobs {
		jobs[i] = sampleJob("id-"+string(rune('a'+i)), job.KindSaleCheck)
	}
	if err := enq.EnqueueBatch(context.Background(), jobs); err != nil {
		t.Fatalf("EnqueueBatch: %v", err)
	}
	if len(s.batches) != 2 {
		t.Fatalf("batch count = %d, want 2", len(s.batches))
	}
	if len(s.batches[0]) != 10 || len(s.batches[1]) != 2 {
		t.Errorf("batch sizes = %d, %d, want 10, 2", len(s.batches[0]), len(s.batches[1]))
	}
}

func TestEnqueueBatch_FailedEntryReturnsError(t *testing.T) {
	s := &stubSQS{failIDs: map[string]string{"0": "InternalError"}}
	enq := NewEnqueuer(s, "queue-url", nil)
	err := enq.Enqueue(context.Background(), sampleJob("sale:c:B0FAIL00001", job.KindSaleCheck))
	if err == nil {
		t.Fatal("want error for failed entry")
	}
}

func TestEnqueueBatch_PropagatesSendError(t *testing.T) {
	s := &stubSQS{failErr: errors.New("network")}
	enq := NewEnqueuer(s, "queue-url", nil)
	if err := enq.Enqueue(context.Background(), sampleJob("sale:c:B0NETERR0001", job.KindSaleCheck)); err == nil {
		t.Fatal("want error for send failure")
	}
}

func TestDeduplicationID_IsSHA256LowerHex(t *testing.T) {
	got := DeduplicationID("abc")
	want := sha256sum("abc")
	if len(got) != 64 {
		t.Errorf("len = %d, want 64", len(got))
	}
	if got != want {
		t.Errorf("DeduplicationID = %q, want %q", got, want)
	}
}

func sha256sum(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
