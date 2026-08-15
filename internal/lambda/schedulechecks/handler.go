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
	"github.com/shinderuman/kindle-automation/internal/logging"
	"github.com/shinderuman/kindle-automation/internal/notification"
)

// Lambda event の source 値。EventBridge Scheduler と CloudWatch Alarm 直接 invoke を判別する。
const (
	sourceScheduler  = "scheduler"
	sourceCloudWatch = "aws.cloudwatch"
)

// Scheduler は Scheduler イベントを dispatch.Run へ、CloudWatch Alarm を Slack error channel 通知へ振り分ける。
type Scheduler struct {
	Deps        dispatch.Dependencies
	ErrorSender notification.Sender // Slack error channel。nil ならログのみ。
	Logger      *slog.Logger
}

// HandleSchedule は投入結果を cycle_dispatched（Checker 無効時は cycle_disabled）へ記録する（SPECIFICATION.md 18.3）。
func (s *Scheduler) HandleSchedule(ctx context.Context, event dispatch.Event) error {
	result, err := dispatch.Run(ctx, s.Deps, event)
	if err != nil {
		return err
	}
	s.logCycle(ctx, event, result)
	return nil
}

// logCycle は Disabled のとき cycle_disabled、それ以外は cycle_dispatched へ
// target_count/enqueued_count を含める（SPECIFICATION.md 18.3）。
func (s *Scheduler) logCycle(ctx context.Context, event dispatch.Event, result dispatch.DispatchResult) {
	if s.Logger == nil {
		return
	}
	attrs := []slog.Attr{
		slog.String("check_type", string(event.CheckType)),
		slog.String("cycle_id", result.CycleID),
		slog.Int("slot_index", result.SlotIndex),
		slog.Int("slot_count", result.SlotCount),
	}
	if result.Disabled {
		s.Logger.LogAttrs(ctx, slog.LevelInfo, logging.EventCycleDisabled, attrs...)
		return
	}
	attrs = append(attrs,
		slog.Int("cycle_target_count", result.CycleTargetCount),
		slog.Int("target_count", result.TargetCount),
		slog.Int("enqueued_count", result.EnqueuedCount),
	)
	s.Logger.LogAttrs(ctx, slog.LevelInfo, logging.EventCycleDispatched, attrs...)
}

// 通知失敗は error を返し Lambda 経由で再試行させる。ErrorSender が未設定ならログ記録のみ。
func (s *Scheduler) HandleAlarm(ctx context.Context, alarm alarmNotificationInput) error {
	message := buildAlarmMessage(alarm)
	if s.Logger != nil {
		s.Logger.LogAttrs(ctx, slog.LevelInfo, logging.EventAlarmNotification,
			slog.String("alarm_name", alarm.AlarmName),
			slog.String("state", alarm.StateValue),
		)
	}
	if s.ErrorSender == nil {
		return nil
	}
	if err := s.ErrorSender.Send(ctx, message); err != nil {
		if s.Logger != nil {
			s.Logger.LogAttrs(ctx, slog.LevelError, logging.EventAlarmNotification,
				slog.String("alarm_name", alarm.AlarmName),
				slog.String("state", alarm.StateValue),
				slog.String("error", err.Error()),
			)
		}
		return fmt.Errorf("alarm notify: %w", err)
	}
	return nil
}

// HandleEvent は source でイベントを判別する。EventBridge Scheduler は source=scheduler、
// CloudWatch Alarm 直接 invoke は source=aws.cloudwatch。
func (s *Scheduler) HandleEvent(ctx context.Context, raw json.RawMessage) error {
	var peek struct {
		Source string `json:"source"`
	}
	if err := json.Unmarshal(raw, &peek); err != nil {
		s.logTerminal(ctx, "event_decode_failed", "", err)
		return fmt.Errorf("decode event source: %w", err)
	}
	switch peek.Source {
	case sourceScheduler:
		event, err := parseScheduleInput(raw)
		if err != nil {
			s.logTerminal(ctx, "schedule_input_invalid", "", err)
			return err
		}
		return s.HandleSchedule(ctx, event)
	case sourceCloudWatch:
		return s.handleAlarmEvent(ctx, raw)
	default:
		err := fmt.Errorf("unknown event source %q", peek.Source)
		s.logTerminal(ctx, "unknown_event_source", peek.Source, err)
		return err
	}
}

// ALARM/OK 以外（INSUFFICIENT_DATA 等）では通知せず正常終了する。decode/validation 失敗は terminal error として伝播する。
func (s *Scheduler) handleAlarmEvent(ctx context.Context, raw json.RawMessage) error {
	alarm, err := parseAlarmInput(raw)
	if err != nil {
		s.logTerminal(ctx, "alarm_input_invalid", "", err)
		return err
	}
	if alarm.StateValue != AlarmStateAlarm && alarm.StateValue != AlarmStateOK {
		// event 名は replaceAttr が msg を "event" key へ map するため msg へ渡す（event attr の併用は重複 key になる）。
		if s.Logger != nil {
			s.Logger.LogAttrs(ctx, slog.LevelInfo, "alarm_state_ignored",
				slog.String("alarm_name", alarm.AlarmName),
				slog.String("state", alarm.StateValue),
			)
		}
		return nil
	}
	return s.HandleAlarm(ctx, alarm)
}

// logTerminal は decode/validation 失敗など再試行無意味な terminal 起動を ERROR で記録する。
// eventToken は SPECIFICATION.md 18.1 の固定 event 名として出力する分類トークン。
// logging の replaceAttr が msg を "event" key へ map するため eventToken を msg へ渡し、
// 別途 "event" attr を併用しない（併用すると event key が重複する）。
// eventSource が空でなければ source field を添える。
func (s *Scheduler) logTerminal(ctx context.Context, eventToken, eventSource string, err error) {
	if s.Logger == nil {
		return
	}
	attrs := []slog.Attr{
		slog.String("result", "terminal"),
		slog.String("error", err.Error()),
	}
	if eventSource != "" {
		attrs = append(attrs, slog.String("source", eventSource))
	}
	s.Logger.LogAttrs(ctx, slog.LevelError, eventToken, attrs...)
}
