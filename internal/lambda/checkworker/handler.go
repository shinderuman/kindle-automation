// Package checkworker は check-worker Lambda の composition root である。
// SQS イベントの decode、job kind ごとのユースケース振り分け、AWS/config 依存組み立て、
// amazon/storage と各 application ユースケース間の bridge（DTO 変換・adapter）を担当する。
// 業務判定・HTML selector・S3 merge ロジックは持たない（AGENTS.md 4）。
package checkworker

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambdacontext"
	"github.com/shinderuman/kindle-automation/internal/application/execution"
	"github.com/shinderuman/kindle-automation/internal/application/newrelease"
	"github.com/shinderuman/kindle-automation/internal/application/papertokindle"
	"github.com/shinderuman/kindle-automation/internal/application/sale"
	"github.com/shinderuman/kindle-automation/internal/gist"
	"github.com/shinderuman/kindle-automation/internal/job"
	"github.com/shinderuman/kindle-automation/internal/logging"
	"github.com/shinderuman/kindle-automation/internal/storage"
)

// Worker は1起動で1つのジョブを処理する。
type Worker struct {
	SaleDeps  sale.Dependencies
	NRDeps    newrelease.Dependencies
	PaperDeps papertokindle.Dependencies
	GistDeps  gist.Dependencies
	// 可変 S3 設定の読み込み元。checker_configs.json・excluded_title_keywords.json は
	// invocation ごとに最新値を読む（refreshVariableConfig）。
	store                    storage.ObjectStore
	checkerConfigKey         string
	excludedTitleKeywordsKey string
	Logger                   *slog.Logger
}

// route の戻り値は、Outcome が結果分類とHTTP計測値、error が再試行させる原因を表す。
func (w *Worker) route(ctx context.Context, j job.Job) (execution.Outcome, error) {
	switch j.Kind {
	case job.KindSaleCheck:
		return sale.HandleSaleCheck(ctx, w.SaleDeps, j)
	case job.KindSaleFinalize:
		return sale.HandleSaleFinalize(ctx, w.SaleDeps, j)
	case job.KindNewReleaseSearch:
		return newrelease.HandleNewReleaseSearch(ctx, w.NRDeps, j)
	case job.KindNewReleaseResult:
		return newrelease.HandleNewReleaseResult(ctx, w.NRDeps, j)
	case job.KindNewReleaseDetail:
		return newrelease.HandleNewReleaseDetail(ctx, w.NRDeps, j)
	case job.KindPaperToKindleCheck:
		return papertokindle.HandlePaperToKindleCheck(ctx, w.PaperDeps, j)
	case job.KindPaperToKindleDetail:
		return papertokindle.HandlePaperToKindleDetail(ctx, w.PaperDeps, j)
	case job.KindGistUpdate:
		return gist.Update(ctx, w.GistDeps, j.Target.GistType)
	default:
		return execution.Errored("unknown_kind", 0, 0), fmt.Errorf("unknown job kind %q", j.Kind)
	}
}

// HandleSQSEvent は各ジョブ結果を固定共通fieldでログへ出し（SPECIFICATION.md 18.1）、
// decode・業務処理の失敗は error として Lambda 経由で SQS へ再配信させる（SPECIFICATION.md 7.2/12）。
func (w *Worker) HandleSQSEvent(ctx context.Context, event events.SQSEvent) error {
	// 可変 S3 設定を invocation ごとに最新へ反映する（cold start に固定しない）。
	// 読込失敗時も record 処理は開始せず Lambda error で SQS 再試行させる既存挙動を維持しつつ、
	// level=ERROR の job_error 構造化ログを1件残す（SPECIFICATION.md 18.1/18.3）。
	if err := w.refreshVariableConfig(ctx); err != nil {
		w.logConfigLoadFailure(ctx, event, err)
		return fmt.Errorf("load variable config: %w", err)
	}
	for _, record := range event.Records {
		j, err := job.Decode([]byte(record.Body))
		if err != nil {
			w.logDecodeFailure(ctx, record, err)
			return fmt.Errorf("decode sqs message %s: %w", record.MessageId, err)
		}
		start := time.Now()
		oc, err := w.route(ctx, j)
		w.logJobResult(ctx, record, j, oc, err, time.Since(start))
		if err != nil {
			return fmt.Errorf("handle job %s: %w", j.JobID, err)
		}
	}
	return nil
}

// logJobResult は result に応じて job_completed/job_terminal/job_error/gist_error へ振り分け、
// 固定共通fieldを出す（SPECIFICATION.md 18.1/18.3）。取得不能な値は string は空、数値は 0、
// http_status は未送信時（0）は空文字列とする。
func (w *Worker) logJobResult(ctx context.Context, record events.SQSMessage, j job.Job, oc execution.Outcome, cause error, duration time.Duration) {
	if w.Logger == nil {
		return
	}
	level, event := levelEventFor(oc.Result, j.Kind)
	errMsg := ""
	if cause != nil {
		errMsg = cause.Error()
	}
	w.Logger.LogAttrs(ctx, level, event,
		slog.String("check_type", string(j.CheckType)),
		slog.String("job_id", j.JobID),
		slog.String("cycle_id", j.CycleID),
		slog.String("target", targetOf(j)),
		slog.Int("receive_count", receiveCount(record)),
		slog.String("result", oc.Result),
		slog.String("error_type", oc.ErrorType),
		slog.String("http_status", httpStatusString(oc.HTTPStatus)),
		slog.Int("duration_ms", int(duration.Milliseconds())),
		slog.Int("response_bytes", oc.ResponseBytes),
		slog.String("aws_request_id", requestID(ctx)),
		slog.String("error", errMsg),
	)
}

// logDecodeFailure は decode 失敗を job_error として出す（SPECIFICATION.md 18.3）。
// job が得られていないため job_id/cycle_id/target/check_type は空になる。
func (w *Worker) logDecodeFailure(ctx context.Context, record events.SQSMessage, err error) {
	if w.Logger == nil {
		return
	}
	w.Logger.LogAttrs(ctx, slog.LevelError, logging.EventJobError,
		slog.String("check_type", ""),
		slog.String("job_id", ""),
		slog.String("cycle_id", ""),
		slog.String("target", ""),
		slog.Int("receive_count", receiveCount(record)),
		slog.String("result", execution.ResultError),
		slog.String("error_type", "decode"),
		slog.String("http_status", ""),
		slog.Int("duration_ms", 0),
		slog.Int("response_bytes", 0),
		slog.String("aws_request_id", requestID(ctx)),
		slog.String("sqs_message_id", record.MessageId),
		slog.String("error", err.Error()),
	)
}

// logConfigLoadFailure は可変設定（checker_configs/excluded_title_keywords）の読込失敗を job_error として出す
// （SPECIFICATION.md 18.1/18.3）。設定読込は record 処理より前に行われるため job は未確定。
// invocation 内の最初の record を best-effort で decode して識別子を埋め、decode 不能でもログ自体は失わない。
// 値の無い http_status/duration_ms/response_bytes は既存契約に従い空・0 とする。raw body・HTML・token・
// 秘密情報は出さない。読込失敗ごとに1回だけ呼ばれ、同一失敗で ERROR を複数出さない。
func (w *Worker) logConfigLoadFailure(ctx context.Context, event events.SQSEvent, err error) {
	if w.Logger == nil {
		return
	}
	jobID, cycleID, checkType, target, recv := bestEffortLogFields(event)
	w.Logger.LogAttrs(ctx, slog.LevelError, logging.EventJobError,
		slog.String("check_type", checkType),
		slog.String("job_id", jobID),
		slog.String("cycle_id", cycleID),
		slog.String("target", target),
		slog.Int("receive_count", recv),
		slog.String("result", execution.ResultError),
		slog.String("error_type", "config_load"),
		slog.String("http_status", ""),
		slog.Int("duration_ms", 0),
		slog.Int("response_bytes", 0),
		slog.String("aws_request_id", requestID(ctx)),
		slog.String("error", err.Error()),
	)
}

// bestEffortLogFields は設定読込失敗ログへ埋める識別子を invocation 内の最初の record から best-effort で取り出す。
// 設定読込は record 処理より前のため job は未確定。decode 不能・record 無しの場合は識別子を空（receive_count は0）
// とし、呼び出し側は識別子が空でもログ自体を失わない。raw body は返さず識別子だけを返す。
func bestEffortLogFields(event events.SQSEvent) (jobID, cycleID, checkType, target string, recv int) {
	if len(event.Records) == 0 {
		return "", "", "", "", 0
	}
	record := event.Records[0]
	j, err := job.Decode([]byte(record.Body))
	if err != nil {
		return "", "", "", "", receiveCount(record)
	}
	return j.JobID, j.CycleID, string(j.CheckType), targetOf(j), receiveCount(record)
}

// levelEventFor は gist_update の失敗を gist_error、それ以外の処理エラーを job_error へ振り分ける（SPECIFICATION.md 18.3）。
func levelEventFor(result string, kind job.Kind) (slog.Level, string) {
	switch result {
	case execution.ResultCompleted:
		return slog.LevelInfo, logging.EventJobCompleted
	case execution.ResultTerminal:
		return slog.LevelWarn, logging.EventJobTerminal
	default:
		if kind == job.KindGistUpdate {
			return slog.LevelError, logging.EventGistError
		}
		return slog.LevelError, logging.EventJobError
	}
}

func targetOf(j job.Job) string {
	switch {
	case j.Target.ASIN != "":
		return j.Target.ASIN
	case j.Target.AuthorName != "":
		return j.Target.AuthorName
	case j.Target.GistType != "":
		return j.Target.GistType
	default:
		return ""
	}
}

func receiveCount(record events.SQSMessage) int {
	if v, ok := record.Attributes["ApproximateReceiveCount"]; ok {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return 0
}

// httpStatusString は HTTP status 未送信（0）の時は空文字列とする（SPECIFICATION.md 18.1）。
func httpStatusString(status int) string {
	if status == 0 {
		return ""
	}
	return strconv.Itoa(status)
}

func requestID(ctx context.Context) string {
	if lctx, ok := lambdacontext.FromContext(ctx); ok {
		return lctx.AwsRequestID
	}
	return ""
}
