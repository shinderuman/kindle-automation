package schedulechecks

import (
	"errors"
	"testing"
	"time"

	"github.com/shinderuman/kindle-automation/internal/job"
)

func TestParseScheduleInput_Valid(t *testing.T) {
	body := `{"version":1,"source":"scheduler","check_type":"sale","scheduled_at":"2026-08-09T00:00:00Z"}`
	got, err := parseScheduleInput([]byte(body))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got.CheckType != job.CheckSale {
		t.Errorf("CheckType = %q", got.CheckType)
	}
	want := time.Date(2026, 8, 9, 0, 0, 0, 0, time.UTC)
	if !got.ScheduledAt.Equal(want) {
		t.Errorf("ScheduledAt = %v, want %v", got.ScheduledAt, want)
	}
}

func TestParseScheduleInput_RejectsInvalid(t *testing.T) {
	cases := map[string]string{
		"bad version":   `{"version":2,"source":"scheduler","check_type":"sale","scheduled_at":"2026-08-09T00:00:00Z"}`,
		"bad source":    `{"version":1,"source":"x","check_type":"sale","scheduled_at":"2026-08-09T00:00:00Z"}`,
		"unknown check": `{"version":1,"source":"scheduler","check_type":"bogus","scheduled_at":"2026-08-09T00:00:00Z"}`,
		"missing time":  `{"version":1,"source":"scheduler","check_type":"sale"}`,
		"broken json":   `not-json`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := parseScheduleInput([]byte(body)); !errors.Is(err, ErrInvalidScheduleInput) {
				t.Fatalf("err = %v, want ErrInvalidScheduleInput", err)
			}
		})
	}
}
