// Package scheduling は周回識別子とジョブ識別子の決定的生成を提供する。
// いずれも外部サービスへ依存しない純粋関数で、同じ入力からは常に同じ文字列を返す。
package scheduling

import (
	"crypto/sha256"
	"encoding/hex"
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
