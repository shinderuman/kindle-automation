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

func TestParseAlarmInput_Valid(t *testing.T) {
	body := `{"source":"aws.cloudwatch","alarmArn":"arn:aws:cloudwatch:us-east-1:111122223333:alarm:kindle-automation-work-dlq","accountId":"111122223333","time":"2026-08-04T12:36:15.490+0000","region":"us-east-1","alarmData":{"alarmName":"kindle-automation-work-dlq","state":{"value":"ALARM","reason":"test","timestamp":"2026-08-04T12:36:15.490+0000"},"previousState":{"value":"OK","reason":"","timestamp":"2026-08-04T12:31:29.595+0000"}}}`
	name, state, err := parseAlarmInput([]byte(body))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if name != "kindle-automation-work-dlq" {
		t.Errorf("alarmName = %q", name)
	}
	if state != "ALARM" {
		t.Errorf("state = %q, want ALARM", state)
	}
}

func TestParseAlarmInput_RejectsInvalid(t *testing.T) {
	cases := map[string]string{
		"missing alarmName": `{"source":"aws.cloudwatch","alarmData":{"state":{"value":"ALARM"}}}`,
		"missing state":     `{"source":"aws.cloudwatch","alarmData":{"alarmName":"x"}}`,
		"empty alarmData":   `{"source":"aws.cloudwatch","alarmData":{}}`,
		"no alarmData":      `{"source":"aws.cloudwatch"}`,
		"broken json":       `not-json`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if _, _, err := parseAlarmInput([]byte(body)); !errors.Is(err, ErrInvalidAlarmInput) {
				t.Fatalf("err = %v, want ErrInvalidAlarmInput", err)
			}
		})
	}
}
