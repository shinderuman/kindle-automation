// Package scheduling は周回識別子とジョブ識別子の決定的生成を提供する。
// いずれも外部サービスへ依存しない純粋関数で、同じ入力からは常に同じ文字列を返す。
package scheduling

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"time"
)

// CycleID は周回識別子を {check_type}:{scheduled_time_utc} 形式で生成する（SPECIFICATION.md 6）。
// scheduledTime は UTC へ正規化する。Scheduler 再試行で同じ時刻を渡せば同じ値になる。
func CycleID(checkType string, scheduledTime time.Time) string {
	return checkType + ":" + scheduledTime.UTC().Format(time.RFC3339)
}

// JobID はジョブ識別子を kind, cycleID, targetID から決定的に生成する（SPECIFICATION.md 7.2）。
// targetID は呼び出し側で正規化済みの対象識別子（ASIN や正規化作者名）を渡すこと。
func JobID(kind string, cycleID string, targetID string) string {
	return kind + ":" + cycleID + ":" + targetID
}

// DedupID は SQS MessageDeduplicationId 用に job_id の SHA-256 小文字hex 64文字を返す（SPECIFICATION.md 7.2）。
func DedupID(jobID string) string {
	sum := sha256.Sum256([]byte(jobID))
	return hex.EncodeToString(sum[:])
}

// WindowSlot はJST基準の周期窓開始時刻と、窓内の実行slotを返す。
func WindowSlot(scheduledAt time.Time, window, interval time.Duration) (time.Time, int, int, error) {
	if window <= 0 || interval <= 0 || window%interval != 0 {
		return time.Time{}, 0, 0, fmt.Errorf("invalid window %s or interval %s", window, interval)
	}
	const jstOffset = 9 * time.Hour
	utc := scheduledAt.UTC()
	cycleStart := utc.Add(jstOffset).Truncate(window).Add(-jstOffset)
	slotCount := int(window / interval)
	slotIndex := int(utc.Sub(cycleStart) / interval)
	if slotIndex < 0 || slotIndex >= slotCount {
		return time.Time{}, 0, 0, fmt.Errorf("scheduled time %s is outside cycle window", scheduledAt.Format(time.RFC3339))
	}
	return cycleStart, slotIndex, slotCount, nil
}

// ShardIndex は同じ対象を常に同じslotへ割り当てる。
func ShardIndex(target string, shardCount int) (int, error) {
	if shardCount <= 0 {
		return 0, fmt.Errorf("invalid shard count %d", shardCount)
	}
	sum := sha256.Sum256([]byte(target))
	return int(binary.BigEndian.Uint64(sum[:8]) % uint64(shardCount)), nil
}
