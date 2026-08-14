// Package logging は SPECIFICATION.md 18 の構造化JSONログを提供する。
//
// log/slog の JSONHandler を使い、共通field名を SPEC 18.1 へ整える。
//   - time ではなく UTC の timestamp
//   - msg ではなく event（固定イベント名は本packageの Event* 定数を使う）
//   - level は INFO / WARN / ERROR（CloudWatch Logs metric filter は level=ERROR を集計する）
//
// 秘密情報（Cookie, Authorization, token, HTML本文）は呼び出し側が渡さないこと。
// 本packageは出力へ値を追加しないため、渡された値だけが出力される。
package logging

import (
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"
)

// 固定イベント名（SPECIFICATION.md 18.3）。
// 周回・job・通知・Gist・Alarmの各結果を表す。metric filter は level=ERROR で集計するため、
// これらのイベント名自体は metric 分割に使わない（18.2）。
const (
	// EventCycleDispatched は周回が対象 job の投入を完了したことを記録する（target_count・enqueued_count を含む）。
	EventCycleDispatched = "cycle_dispatched"
	// EventCycleDisabled は Checker 設定で check_type が無効なため投入を省略したことを記録する。
	EventCycleDisabled = "cycle_disabled"
	// EventJobCompleted は job が再試行を要しない正常結果で終わったことを記録する。
	EventJobCompleted = "job_completed"
	// EventJobTerminal は 404・対象種別不一致など再試行無意味なターミナル結果であることを記録する。
	EventJobTerminal = "job_terminal"
	// EventJobError は SQS 再試行させる job 処理エラーであることを記録する。
	EventJobError = "job_error"
	// EventNotificationError は Slack・Mastodon 等の通知送信エラーであることを記録する。
	EventNotificationError = "notification_error"
	// EventGistError は GitHub Gist 更新ジョブのエラーであることを記録する（gist_update 専用）。
	EventGistError = "gist_error"
	// EventAlarmNotification は CloudWatch Alarm の Slack error channel 通知処理の結果を記録する（SPECIFICATION.md 17.2）。
	EventAlarmNotification = "alarm_notification"
)

// New は SPECIFICATION.md 18.1 に従う JSON構造化loggerを構築する。
// 第1引数へ CloudWatch Logs へ転送される Lambda の stdout 等を渡す。
// 依存方向に従い、環境変数の読み取りは呼び出し側(config)へ任せ、level は引数で受ける。
func New(w io.Writer, level slog.Level) *slog.Logger {
	h := slog.NewJSONHandler(w, &slog.HandlerOptions{
		Level:       level,
		ReplaceAttr: replaceAttr,
	})
	return slog.New(h)
}

// replaceAttr は組み込みfield名と時刻表現を SPEC 18.1 へ整える。
// time を UTC の timestamp へ、msg を event へ変更し、level は文字列表現へ統一する。
func replaceAttr(groups []string, a slog.Attr) slog.Attr {
	if len(groups) > 0 {
		return a
	}
	switch a.Key {
	case slog.TimeKey:
		a.Key = "timestamp"
		if t, ok := a.Value.Any().(time.Time); ok {
			a.Value = slog.StringValue(t.UTC().Format(time.RFC3339Nano))
		}
	case slog.LevelKey:
		// 標準レベルは "INFO"/"WARN"/"ERROR" へ統一する（SPEC 18.1）。
		if lv, ok := a.Value.Any().(slog.Level); ok {
			a.Value = slog.StringValue(lv.String())
		}
	case slog.MessageKey:
		a.Key = "event"
	}
	return a
}

// ParseLevel はログレベル文字列を slog.Level へ変換する（SPECIFICATION.md 8/16）。
// 大文字小文字と前後空白を許容する。不正値を暗黙に default 補完せず error を返す。
func ParseLevel(s string) (slog.Level, error) {
	switch strings.ToUpper(strings.TrimSpace(s)) {
	case "DEBUG":
		return slog.LevelDebug, nil
	case "INFO":
		return slog.LevelInfo, nil
	case "WARN":
		return slog.LevelWarn, nil
	case "ERROR":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("invalid log level %q: want DEBUG, INFO, WARN, or ERROR", s)
	}
}
