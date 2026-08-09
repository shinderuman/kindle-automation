// Package schedulechecks は schedule-checks Lambda の composition root である。
// EventBridge Scheduler イベントの decode と検証、Scheduler/CloudWatch Alarm の振り分け、
// AWS/config 依存組み立て、dispatch ユースケースへ渡す adapter を担当する。
// 業務判定・HTML selector・S3 merge・Amazon 取得は行わない（SPECIFICATION.md 5.1, AGENTS.md 4）。
package schedulechecks

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/shinderuman/kindle-automation/internal/application/dispatch"
	"github.com/shinderuman/kindle-automation/internal/domain/scheduling"
	"github.com/shinderuman/kindle-automation/internal/logging"
	"github.com/shinderuman/kindle-automation/internal/notification"
)

// Scheduler は schedule-checks Lambda の振る舞いを保持する。
// Scheduler イベントは dispatch.Run へ、CloudWatch Alarm イベントは Slack error channel 通知へ振り分ける。
type Scheduler struct {
	Deps        dispatch.Dependencies
	ErrorSender notification.Sender // Slack error channel。nil ならログのみ。
	Logger      *slog.Logger
}

// HandleSchedule は1周分の対象ジョブ化と SQS 投入を行う。
// 投入結果は cycle_dispatched（または Checker 無効時は cycle_disabled）へ記録する（SPECIFICATION.md 18.3）。
func (s *Scheduler) HandleSchedule(ctx context.Context, event dispatch.Event) error {
	result, err := dispatch.Run(ctx, s.Deps, event)
	if err != nil {
		return err
	}
	s.logCycle(ctx, event, result)
	return nil
}

// logCycle は1周の dispatch 結果を構造化ログへ出す（SPECIFICATION.md 18.3）。
// Disabled のときは cycle_disabled、それ以外は cycle_dispatched へ
// target_count/enqueued_count/upcoming_merged を含める。
func (s *Scheduler) logCycle(ctx context.Context, event dispatch.Event, result dispatch.DispatchResult) {
	if s.Logger == nil {
		return
	}
	attrs := []slog.Attr{
		slog.String("check_type", string(event.CheckType)),
		slog.String("cycle_id", scheduling.CycleID(string(event.CheckType), event.ScheduledAt)),
	}
	if result.Disabled {
		s.Logger.LogAttrs(ctx, slog.LevelInfo, logging.EventCycleDisabled, attrs...)
		return
	}
	attrs = append(attrs,
		slog.Int("target_count", result.TargetCount),
		slog.Int("enqueued_count", result.EnqueuedCount),
		slog.Int("upcoming_merged", result.UpcomingMerged),
	)
	s.Logger.LogAttrs(ctx, slog.LevelInfo, logging.EventCycleDispatched, attrs...)
}

// HandleAlarm は CloudWatch Alarm の ALARM 遷移を Slack error channel へ1件通知する（SPECIFICATION.md 5.1）。
// 通知失敗は error を返し Lambda 経由で再試行させる。ErrorSender が未設定ならログ記録のみ。
func (s *Scheduler) HandleAlarm(ctx context.Context, alarmName string) error {
	message := fmt.Sprintf("🚨 CloudWatch Alarm 発報: %s", alarmName)
	if s.Logger != nil {
		s.Logger.LogAttrs(ctx, slog.LevelInfo, logging.EventAlarmNotification,
			slog.String("alarm_name", alarmName),
		)
	}
	if s.ErrorSender == nil {
		return nil
	}
	if err := s.ErrorSender.Send(ctx, message); err != nil {
		if s.Logger != nil {
			s.Logger.LogAttrs(ctx, slog.LevelError, logging.EventAlarmNotification,
				slog.String("alarm_name", alarmName),
				slog.String("error", err.Error()),
			)
		}
		return fmt.Errorf("alarm notify: %w", err)
	}
	return nil
}

// HandleEvent は Lambda へ渡された生イベントを source で判別し Scheduler/Alarm へ振り分ける。
// EventBridge Scheduler の定数入力は source=scheduler。それ以外は CloudWatch Alarm とみなす。
func (s *Scheduler) HandleEvent(ctx context.Context, raw json.RawMessage) error {
	var peek struct {
		Source    string `json:"source"`
		AlarmName string `json:"AlarmName"`
	}
	_ = json.Unmarshal(raw, &peek)
	if peek.Source == "scheduler" {
		event, err := parseScheduleInput(raw)
		if err != nil {
			if s.Logger != nil {
				s.Logger.ErrorContext(ctx, "schedule input invalid",
					slog.String("event", "schedule_input_invalid"),
					slog.String("result", "terminal"),
					slog.String("error", err.Error()),
				)
			}
			return err
		}
		return s.HandleSchedule(ctx, event)
	}
	return s.HandleAlarm(ctx, peek.AlarmName)
}
