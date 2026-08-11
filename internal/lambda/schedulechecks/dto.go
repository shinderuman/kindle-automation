package schedulechecks

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/shinderuman/kindle-automation/internal/application/dispatch"
	"github.com/shinderuman/kindle-automation/internal/job"
)

// scheduleInput は EventBridge Scheduler から schedule-checks へ渡される定数入力（SPECIFICATION.md 6）。
type scheduleInput struct {
	Version     int           `json:"version"`
	Source      string        `json:"source"`
	CheckType   job.CheckType `json:"check_type"`
	ScheduledAt time.Time     `json:"scheduled_at"`
}

// ErrInvalidScheduleInput は EventBridge Scheduler 入力の decode・検証失敗（再試行無意味な terminal 入力）を示す。
var ErrInvalidScheduleInput = errors.New("invalid schedule input")

// ErrInvalidAlarmInput は CloudWatch Alarm 直接 invoke payload の decode・検証失敗（再試行無意味な terminal 入力）を示す。
var ErrInvalidAlarmInput = errors.New("invalid alarm input")

// CloudWatch Alarm が AlarmActions で schedule-checks を直接 invoke した時の payload（SPECIFICATION.md 17.2）。
// source=aws.cloudwatch、alarmData 配下へ alarmName と state.value が入る（AWS 直接 invoke 形式）。
// SNS/EventBridge 経由ではないためトップレベルの AlarmName や detail 型にはならない。
type alarmInput struct {
	Source    string    `json:"source"`
	AlarmData alarmData `json:"alarmData"`
}

type alarmData struct {
	AlarmName string     `json:"alarmName"`
	State     alarmState `json:"state"`
}

type alarmState struct {
	Value string `json:"value"`
}

// AlarmStateAlarm は CloudWatch Alarm の state.value が ALARM へ遷移したことを表す（SPECIFICATION.md 17.2）。
const AlarmStateAlarm = "ALARM"

// parseAlarmInput は alarmName・state.value 欠落を再試行無意味な terminal 入力として ErrInvalidAlarmInput を返す。
func parseAlarmInput(data []byte) (alarmName, stateValue string, err error) {
	var in alarmInput
	if err := json.Unmarshal(data, &in); err != nil {
		return "", "", fmt.Errorf("%w: decode: %v", ErrInvalidAlarmInput, err)
	}
	if in.AlarmData.AlarmName == "" {
		return "", "", fmt.Errorf("%w: alarmData.alarmName missing", ErrInvalidAlarmInput)
	}
	if in.AlarmData.State.Value == "" {
		return "", "", fmt.Errorf("%w: alarmData.state.value missing", ErrInvalidAlarmInput)
	}
	return in.AlarmData.AlarmName, in.AlarmData.State.Value, nil
}

func parseScheduleInput(data []byte) (dispatch.Event, error) {
	var in scheduleInput
	if err := json.Unmarshal(data, &in); err != nil {
		return dispatch.Event{}, fmt.Errorf("%w: decode: %v", ErrInvalidScheduleInput, err)
	}
	if in.Version != 1 {
		return dispatch.Event{}, fmt.Errorf("%w: version %d", ErrInvalidScheduleInput, in.Version)
	}
	if in.Source != "scheduler" {
		return dispatch.Event{}, fmt.Errorf("%w: source %q", ErrInvalidScheduleInput, in.Source)
	}
	switch in.CheckType {
	case job.CheckSale, job.CheckNewRelease, job.CheckPaperToKindle:
	default:
		return dispatch.Event{}, fmt.Errorf("%w: check_type %q", ErrInvalidScheduleInput, in.CheckType)
	}
	if in.ScheduledAt.IsZero() {
		return dispatch.Event{}, fmt.Errorf("%w: scheduled_at missing", ErrInvalidScheduleInput)
	}
	return dispatch.Event{CheckType: in.CheckType, ScheduledAt: in.ScheduledAt}, nil
}
