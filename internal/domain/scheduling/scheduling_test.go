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
	if DedupID("a") == DedupID("b") {
		t.Fatalf("DedupID must differ for different job_id")
	}
}

func TestJobID_DiscriminatesByKindCycleTarget(t *testing.T) {
	cycle := "sale:2026-07-23T00:00:00Z"
	base := JobID("sale_check", cycle, "B0FX3X569X")

	if JobID("sale_check", cycle, "B0FX3X569X") != base {
		t.Fatalf("same inputs must yield same job_id")
	}
	if JobID("paper_to_kindle_check", cycle, "B0FX3X569X") == base {
		t.Errorf("different kind must yield different job_id")
	}
	if JobID("sale_check", "sale:2026-07-23T02:00:00Z", "B0FX3X569X") == base {
		t.Errorf("different cycle must yield different job_id")
	}
	if JobID("sale_check", cycle, "B0OTHER0001") == base {
		t.Errorf("different target must yield different job_id")
	}
}

func TestWindowSlot_UsesJSTCycleBoundaries(t *testing.T) {
	jst := time.FixedZone("JST", 9*60*60)
	tests := []struct {
		name      string
		at        time.Time
		window    time.Duration
		wantStart time.Time
		wantSlot  int
		wantCount int
	}{
		{
			name:      "sale cycle first slot",
			at:        time.Date(2026, 8, 15, 0, 0, 0, 0, jst),
			window:    2 * time.Hour,
			wantStart: time.Date(2026, 8, 14, 15, 0, 0, 0, time.UTC),
			wantSlot:  0,
			wantCount: 24,
		},
		{
			name:      "sale cycle final slot",
			at:        time.Date(2026, 8, 15, 1, 55, 0, 0, jst),
			window:    2 * time.Hour,
			wantStart: time.Date(2026, 8, 14, 15, 0, 0, 0, time.UTC),
			wantSlot:  23,
			wantCount: 24,
		},
		{
			name:      "sale next cycle",
			at:        time.Date(2026, 8, 15, 2, 0, 0, 0, jst),
			window:    2 * time.Hour,
			wantStart: time.Date(2026, 8, 14, 17, 0, 0, 0, time.UTC),
			wantSlot:  0,
			wantCount: 24,
		},
		{
			name:      "new release offset first slot",
			at:        time.Date(2026, 8, 15, 0, 1, 0, 0, jst),
			window:    6 * time.Hour,
			wantStart: time.Date(2026, 8, 14, 15, 0, 0, 0, time.UTC),
			wantSlot:  0,
			wantCount: 72,
		},
		{
			name:      "paper offset final slot",
			at:        time.Date(2026, 8, 15, 5, 57, 0, 0, jst),
			window:    6 * time.Hour,
			wantStart: time.Date(2026, 8, 14, 15, 0, 0, 0, time.UTC),
			wantSlot:  71,
			wantCount: 72,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			start, slot, count, err := WindowSlot(tc.at, tc.window, 5*time.Minute)
			if err != nil {
				t.Fatalf("WindowSlot: %v", err)
			}
			if !start.Equal(tc.wantStart) || slot != tc.wantSlot || count != tc.wantCount {
				t.Fatalf("WindowSlot = (%s, %d, %d), want (%s, %d, %d)", start, slot, count, tc.wantStart, tc.wantSlot, tc.wantCount)
			}
		})
	}
}

func TestWindowSlot_IsTimezoneIndependent(t *testing.T) {
	moment := time.Date(2026, 8, 14, 16, 55, 0, 0, time.UTC)
	jst := time.FixedZone("JST", 9*60*60)
	startUTC, slotUTC, countUTC, err := WindowSlot(moment, 2*time.Hour, 5*time.Minute)
	if err != nil {
		t.Fatalf("WindowSlot UTC: %v", err)
	}
	startJST, slotJST, countJST, err := WindowSlot(moment.In(jst), 2*time.Hour, 5*time.Minute)
	if err != nil {
		t.Fatalf("WindowSlot JST: %v", err)
	}
	if !startUTC.Equal(startJST) || slotUTC != slotJST || countUTC != countJST {
		t.Fatalf("same instant differs: UTC=(%s,%d,%d) JST=(%s,%d,%d)", startUTC, slotUTC, countUTC, startJST, slotJST, countJST)
	}
}

func TestWindowSlot_RejectsInvalidDurations(t *testing.T) {
	for _, tc := range []struct {
		window   time.Duration
		interval time.Duration
	}{
		{window: 0, interval: 5 * time.Minute},
		{window: 2 * time.Hour, interval: 0},
		{window: 2 * time.Hour, interval: 7 * time.Minute},
	} {
		if _, _, _, err := WindowSlot(time.Now(), tc.window, tc.interval); err == nil {
			t.Errorf("WindowSlot(%s, %s) should fail", tc.window, tc.interval)
		}
	}
}

func TestShardIndex_IsDeterministicAndBounded(t *testing.T) {
	for _, count := range []int{1, 24, 72} {
		got, err := ShardIndex("B0FX3X569X", count)
		if err != nil {
			t.Fatalf("ShardIndex: %v", err)
		}
		again, err := ShardIndex("B0FX3X569X", count)
		if err != nil {
			t.Fatalf("ShardIndex again: %v", err)
		}
		if got != again || got < 0 || got >= count {
			t.Fatalf("ShardIndex count=%d got=%d again=%d", count, got, again)
		}
	}
	if _, err := ShardIndex("B0FX3X569X", 0); err == nil {
		t.Fatal("ShardIndex should reject zero shard count")
	}
}
