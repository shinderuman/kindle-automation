package scheduling

import (
	"testing"
	"time"
)

func TestCycleID(t *testing.T) {
	jst := time.FixedZone("JST", 9*60*60)
	scheduled := time.Date(2026, 7, 23, 9, 0, 0, 0, jst)

	tests := []struct {
		name      string
		checkType string
		at        time.Time
		want      string
	}{
		{name: "JST時刻をUTCのRFC3339へ正規化する", checkType: "sale", at: scheduled, want: "sale:2026-07-23T00:00:00Z"},
		{name: "新刊check_type", checkType: "new_release", at: scheduled, want: "new_release:2026-07-23T00:00:00Z"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := CycleID(tc.checkType, tc.at)
			if got != tc.want {
				t.Fatalf("CycleID = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestCycleIDIsDeterministicAcrossTimezones(t *testing.T) {
	jst := time.FixedZone("JST", 9*60*60)
	utc := time.UTC
	moment := time.Date(2026, 7, 23, 0, 0, 0, 0, utc)
	sameMomentJST := moment.In(jst)

	if CycleID("sale", moment) != CycleID("sale", sameMomentJST) {
		t.Fatalf("CycleID must be identical for the same instant in different timezones")
	}
}

func TestJobID(t *testing.T) {
	cycle := "sale:2026-07-23T00:00:00Z"
	got := JobID("sale_check", cycle, "B0FX3X569X")
	want := "sale_check:sale:2026-07-23T00:00:00Z:B0FX3X569X"
	if got != want {
		t.Fatalf("JobID = %q, want %q", got, want)
	}
	// 同じ入力からは同じ値。決定性の検証。
	if JobID("sale_check", cycle, "B0FX3X569X") != got {
		t.Fatalf("JobID is not deterministic")
	}
}

func TestDedupID(t *testing.T) {
	// SHA-256("test") の既知ベクトル。
	got := DedupID("test")
	want := "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"
	if got != want {
		t.Fatalf("DedupID(\"test\") = %q, want %q", got, want)
	}
	if len(got) != 64 {
		t.Fatalf("DedupID length = %d, want 64", len(got))
	}
	// job_id が異なれば DedupID も異なる。
	if DedupID("a") == DedupID("b") {
		t.Fatalf("DedupID must differ for different job_id")
	}
}
