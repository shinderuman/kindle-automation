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

// UpcomingMerger はセール周期開始時の Upcoming→Unprocessed 条件付き merge 抽象（SPECIFICATION.md 10）。
type UpcomingMerger interface {
	MergeUpcoming(ctx context.Context) (int, error)
}

// Dependencies は Run へ注入する dispatch ユースケースの依存セット。
type Dependencies struct {
	AsinListReader AsinListReader
	AuthorReader   AuthorReader
	ConfigReader   ConfigReader
	Enqueuer       Enqueuer
	UpcomingMerger UpcomingMerger
	Keys           Keys
}

// DispatchResult は1周期分のジョブ投入結果（SPECIFICATION.md 18.3 cycle_dispatched/cycle_disabled）。
// Disabled が true のときは Checker 無効で投入を省略したことを表す。
// composition root がこの値から cycle_dispatched または cycle_disabled を出す。
type DispatchResult struct {
	Disabled       bool
	TargetCount    int // 対象件数（dedup 後）。sale は sale_check 対象のみで finalize を含まない。
	EnqueuedCount  int // 実投入 job 数。sale は sale_check + finalize = TargetCount + 1。
	UpcomingMerged int // sale 周回の Upcoming→Unprocessed 取り込み件数。sale 以外は 0。
}

// Run は EventBridge Scheduler イベントをジョブ化して SQS へ投入する。
func Run(ctx context.Context, deps Dependencies, event Event) (DispatchResult, error) {
	cycleID := scheduling.CycleID(string(event.CheckType), event.ScheduledAt)
	enabled, err := deps.ConfigReader.IsEnabled(ctx, event.CheckType)
	if err != nil {
		return DispatchResult{}, fmt.Errorf("load config for %s: %w", event.CheckType, err)
	}
	if !enabled {
		// Checker 無効ならジョブを投入しない（cycle_disabled）。
		return DispatchResult{Disabled: true}, nil
	}
	switch event.CheckType {
	case job.CheckSale:
		return runSale(ctx, deps, event, cycleID)
	case job.CheckNewRelease:
		return runNewRelease(ctx, deps, event, cycleID)
	case job.CheckPaperToKindle:
		return runPaperToKindle(ctx, deps, event, cycleID)
	default:
		return DispatchResult{}, fmt.Errorf("unknown check_type %q", event.CheckType)
	}
}

// Upcoming 取り込み → sale_check → 全件投入成功後に sale_finalize を1件、の順序。
func runSale(ctx context.Context, deps Dependencies, event Event, cycleID string) (DispatchResult, error) {
	merged, err := deps.UpcomingMerger.MergeUpcoming(ctx)
	if err != nil {
		return DispatchResult{}, fmt.Errorf("merge upcoming: %w", err)
	}
	asins, err := deps.AsinListReader.LoadAsins(ctx, deps.Keys.Unprocessed)
	if err != nil {
		return DispatchResult{}, fmt.Errorf("load unprocessed: %w", err)
	}
	deduped := dedup(asins)
	jobs := buildAsinJobs(job.KindSaleCheck, event.CheckType, cycleID, event.ScheduledAt, deduped)
	if err := enqueueBatched(ctx, deps.Enqueuer, jobs); err != nil {
		return DispatchResult{}, fmt.Errorf("enqueue sale_check: %w", err)
	}
	// 対象が空でも Sale だけは sale_finalize を投入し、空リストへ Gist を同期できるようにする（SPECIFICATION.md 6）。
	finalize := buildJob(job.KindSaleFinalize, event.CheckType, cycleID, event.ScheduledAt, finalizeTargetID, job.Target{})
	if err := deps.Enqueuer.EnqueueBatch(ctx, []job.Job{finalize}); err != nil {
		return DispatchResult{}, fmt.Errorf("enqueue sale_finalize: %w", err)
	}
	return DispatchResult{
		TargetCount:    len(deduped),
		EnqueuedCount:  len(deduped) + 1,
		UpcomingMerged: merged,
	}, nil
}

func runNewRelease(ctx context.Context, deps Dependencies, event Event, cycleID string) (DispatchResult, error) {
	names, err := deps.AuthorReader.LoadAuthorNames(ctx, deps.Keys.Authors)
	if err != nil {
		return DispatchResult{}, fmt.Errorf("load authors: %w", err)
	}
	deduped := dedup(names)
	jobs := buildAuthorJobs(job.KindNewReleaseSearch, event.CheckType, cycleID, event.ScheduledAt, deduped)
	if err := enqueueBatched(ctx, deps.Enqueuer, jobs); err != nil {
		return DispatchResult{}, err
	}
	return DispatchResult{TargetCount: len(deduped), EnqueuedCount: len(deduped)}, nil
}

func runPaperToKindle(ctx context.Context, deps Dependencies, event Event, cycleID string) (DispatchResult, error) {
	asins, err := deps.AsinListReader.LoadAsins(ctx, deps.Keys.PaperBooks)
	if err != nil {
		return DispatchResult{}, fmt.Errorf("load paper_books: %w", err)
	}
	deduped := dedup(asins)
	jobs := buildAsinJobs(job.KindPaperToKindleCheck, event.CheckType, cycleID, event.ScheduledAt, deduped)
	if err := enqueueBatched(ctx, deps.Enqueuer, jobs); err != nil {
		return DispatchResult{}, err
	}
	return DispatchResult{TargetCount: len(deduped), EnqueuedCount: len(deduped)}, nil
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
