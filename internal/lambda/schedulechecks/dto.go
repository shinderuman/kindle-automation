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

// ErrInvalidScheduleInput は Scheduler 入力の validation 失敗。
var ErrInvalidScheduleInput = errors.New("invalid schedule input")

// parseScheduleInput は Scheduler 入力を検証して dispatch.Event へ変換する。
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
