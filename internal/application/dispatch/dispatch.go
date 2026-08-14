// Package dispatch は schedule-checks Lambda のユースケースを実装する。
// EventBridge Scheduler イベントから対象をジョブ化し SQS へ投入する。
// Amazon・商品通知・Gist へはアクセスせず、業務判定も行わない（SPECIFICATION.md 5.1, AGENTS.md 4）。
package dispatch

import (
	"context"
	"fmt"
	"time"

	"github.com/shinderuman/kindle-automation/internal/domain/scheduling"
	"github.com/shinderuman/kindle-automation/internal/job"
)

// Event は schedule-checks Lambda への入力イベント（check_type + scheduled_at、SPECIFICATION.md 6）。
type Event struct {
	CheckType   job.CheckType
	ScheduledAt time.Time
}

// Keys は dispatch が読み出す対象リストの S3 object key 集。
type Keys struct {
	Unprocessed string
	Authors     string
	PaperBooks  string
}

// AsinListReader は ASIN リスト S3 object の読み出し抽象。
// unprocessed_asins, paper_books_asins 用。
type AsinListReader interface {
	LoadAsins(ctx context.Context, key string) ([]string, error)
}

// AuthorReader は著者名リスト S3 object の読み出し抽象。
// authors.json の Name 用。
type AuthorReader interface {
	LoadAuthorNames(ctx context.Context, key string) ([]string, error)
}

// ConfigReader は Checker の有効/無効設定の読み出し抽象。
type ConfigReader interface {
	IsEnabled(ctx context.Context, checkType job.CheckType) (bool, error)
}

// Enqueuer はジョブの SQS バッチ投入抽象。
// batch 内の1件でも失敗すれば error を返す（SPECIFICATION.md 7.3）。
type Enqueuer interface {
	EnqueueBatch(ctx context.Context, jobs []job.Job) error
}

// Dependencies は Run へ注入する dispatch ユースケースの依存セット。
type Dependencies struct {
	AsinListReader AsinListReader
	AuthorReader   AuthorReader
	ConfigReader   ConfigReader
	Enqueuer       Enqueuer
	Keys           Keys
}

// DispatchResult は1周期分のジョブ投入結果（SPECIFICATION.md 18.3 cycle_dispatched/cycle_disabled）。
// Disabled が true のときは Checker 無効で投入を省略したことを表す。
// composition root がこの値から cycle_dispatched または cycle_disabled を出す。
type DispatchResult struct {
	Disabled         bool
	CycleID          string
	CycleTargetCount int
	TargetCount      int
	EnqueuedCount    int
	SlotIndex        int
	SlotCount        int
}

// Run は EventBridge Scheduler イベントをジョブ化して SQS へ投入する。
func Run(ctx context.Context, deps Dependencies, event Event) (DispatchResult, error) {
	window, err := cycleWindow(event.CheckType)
	if err != nil {
		return DispatchResult{}, err
	}
	cycleStart, slotIndex, slotCount, err := scheduling.WindowSlot(event.ScheduledAt, window, dispatchInterval)
	if err != nil {
		return DispatchResult{}, fmt.Errorf("resolve dispatch slot: %w", err)
	}
	result := DispatchResult{
		CycleID:   scheduling.CycleID(string(event.CheckType), cycleStart),
		SlotIndex: slotIndex,
		SlotCount: slotCount,
	}
	enabled, err := deps.ConfigReader.IsEnabled(ctx, event.CheckType)
	if err != nil {
		return DispatchResult{}, fmt.Errorf("load config for %s: %w", event.CheckType, err)
	}
	if !enabled {
		result.Disabled = true
		return result, nil
	}
	switch event.CheckType {
	case job.CheckSale:
		return runSale(ctx, deps, event, result)
	case job.CheckNewRelease:
		return runNewRelease(ctx, deps, event, result)
	case job.CheckPaperToKindle:
		return runPaperToKindle(ctx, deps, event, result)
	}
	return DispatchResult{}, fmt.Errorf("unknown check_type %q", event.CheckType)
}

const (
	dispatchInterval = 5 * time.Minute
	saleWindow       = 2 * time.Hour
	standardWindow   = 6 * time.Hour
)

func cycleWindow(checkType job.CheckType) (time.Duration, error) {
	switch checkType {
	case job.CheckSale:
		return saleWindow, nil
	case job.CheckNewRelease, job.CheckPaperToKindle:
		return standardWindow, nil
	default:
		return 0, fmt.Errorf("unknown check_type %q", checkType)
	}
}

func runSale(ctx context.Context, deps Dependencies, event Event, result DispatchResult) (DispatchResult, error) {
	asins, err := deps.AsinListReader.LoadAsins(ctx, deps.Keys.Unprocessed)
	if err != nil {
		return DispatchResult{}, fmt.Errorf("load unprocessed: %w", err)
	}
	deduped := dedup(asins)
	shard, err := selectShard(deduped, result.SlotIndex, result.SlotCount)
	if err != nil {
		return DispatchResult{}, err
	}
	jobs := buildAsinJobs(job.KindSaleCheck, event.CheckType, result.CycleID, event.ScheduledAt, shard)
	if err := enqueueBatched(ctx, deps.Enqueuer, jobs); err != nil {
		return DispatchResult{}, fmt.Errorf("enqueue sale_check: %w", err)
	}
	result.CycleTargetCount = len(deduped)
	result.TargetCount = len(shard)
	result.EnqueuedCount = len(shard)
	if result.SlotIndex == result.SlotCount-1 {
		finalize := buildJob(job.KindSaleFinalize, event.CheckType, result.CycleID, event.ScheduledAt, finalizeTargetID, job.Target{})
		if err := deps.Enqueuer.EnqueueBatch(ctx, []job.Job{finalize}); err != nil {
			return DispatchResult{}, fmt.Errorf("enqueue sale_finalize: %w", err)
		}
		result.EnqueuedCount++
	}
	return result, nil
}

func runNewRelease(ctx context.Context, deps Dependencies, event Event, result DispatchResult) (DispatchResult, error) {
	names, err := deps.AuthorReader.LoadAuthorNames(ctx, deps.Keys.Authors)
	if err != nil {
		return DispatchResult{}, fmt.Errorf("load authors: %w", err)
	}
	deduped := dedup(names)
	shard, err := selectShard(deduped, result.SlotIndex, result.SlotCount)
	if err != nil {
		return DispatchResult{}, err
	}
	jobs := buildAuthorJobs(job.KindNewReleaseSearch, event.CheckType, result.CycleID, event.ScheduledAt, shard)
	if err := enqueueBatched(ctx, deps.Enqueuer, jobs); err != nil {
		return DispatchResult{}, err
	}
	result.CycleTargetCount = len(deduped)
	result.TargetCount = len(shard)
	result.EnqueuedCount = len(shard)
	return result, nil
}

func runPaperToKindle(ctx context.Context, deps Dependencies, event Event, result DispatchResult) (DispatchResult, error) {
	asins, err := deps.AsinListReader.LoadAsins(ctx, deps.Keys.PaperBooks)
	if err != nil {
		return DispatchResult{}, fmt.Errorf("load paper_books: %w", err)
	}
	deduped := dedup(asins)
	shard, err := selectShard(deduped, result.SlotIndex, result.SlotCount)
	if err != nil {
		return DispatchResult{}, err
	}
	jobs := buildAsinJobs(job.KindPaperToKindleCheck, event.CheckType, result.CycleID, event.ScheduledAt, shard)
	if err := enqueueBatched(ctx, deps.Enqueuer, jobs); err != nil {
		return DispatchResult{}, err
	}
	result.CycleTargetCount = len(deduped)
	result.TargetCount = len(shard)
	result.EnqueuedCount = len(shard)
	return result, nil
}

const finalizeTargetID = "finalize"

func buildAsinJobs(kind job.Kind, checkType job.CheckType, cycleID string, scheduledAt time.Time, asins []string) []job.Job {
	jobs := make([]job.Job, 0, len(asins))
	for _, asin := range asins {
		jobs = append(jobs, buildJob(kind, checkType, cycleID, scheduledAt, asin, job.Target{ASIN: asin}))
	}
	return jobs
}

func buildAuthorJobs(kind job.Kind, checkType job.CheckType, cycleID string, scheduledAt time.Time, names []string) []job.Job {
	jobs := make([]job.Job, 0, len(names))
	for _, name := range names {
		jobs = append(jobs, buildJob(kind, checkType, cycleID, scheduledAt, name, job.Target{AuthorName: name}))
	}
	return jobs
}

// job_id は kind+cycleID+targetID で決定的（SPECIFICATION.md 7.2）。
func buildJob(kind job.Kind, checkType job.CheckType, cycleID string, scheduledAt time.Time, targetID string, target job.Target) job.Job {
	return job.Job{
		Version:     job.Version,
		JobID:       scheduling.JobID(string(kind), cycleID, targetID),
		Kind:        kind,
		CheckType:   checkType,
		CycleID:     cycleID,
		ScheduledAt: scheduledAt,
		Target:      target,
	}
}

// enqueueBatched は10件単位で順番に送信する（SPECIFICATION.md 7.3）。
func enqueueBatched(ctx context.Context, enq Enqueuer, jobs []job.Job) error {
	for i := 0; i < len(jobs); i += 10 {
		end := i + 10
		if end > len(jobs) {
			end = len(jobs)
		}
		if err := enq.EnqueueBatch(ctx, jobs[i:end]); err != nil {
			return err
		}
	}
	return nil
}

func dedup(values []string) []string {
	seen := make(map[string]struct{})
	out := make([]string, 0, len(values))
	for _, v := range values {
		if v == "" {
			continue
		}
		if _, exists := seen[v]; exists {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out
}

func selectShard(values []string, slotIndex, slotCount int) ([]string, error) {
	out := make([]string, 0, len(values)/slotCount+1)
	for _, value := range values {
		index, err := scheduling.ShardIndex(value, slotCount)
		if err != nil {
			return nil, fmt.Errorf("assign dispatch shard: %w", err)
		}
		if index == slotIndex {
			out = append(out, value)
		}
	}
	return out, nil
}
