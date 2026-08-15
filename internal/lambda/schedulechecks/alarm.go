package schedulechecks

import (
	"fmt"
	"strings"
)

const unknownFieldText = "不明"

// 既知3 Alarm の AlarmName（infra/template.yaml の AlarmName と一致、SPECIFICATION.md 17.2）。
const (
	alarmNameWorkDLQ        = "kindle-automation-work-dlq"
	alarmNameSchedulerDLQ   = "kindle-automation-scheduler-dlq"
	alarmNameWorkQueueStall = "kindle-automation-work-queue-stall"
)

type alarmGuide struct {
	whatHappened string
	actionNeeded string
	steps        []string
}

func guideForAlarm(name string) (alarmGuide, bool) {
	switch name {
	case alarmNameWorkDLQ:
		return alarmGuide{
			whatHappened: "Work Queue DLQ にメッセージが溜まりました",
			actionNeeded: "対応必要",
			steps: []string{
				"1. check-worker の job_error ログと DLQ message の対象 job を確認する",
				"2. 原因解消後に DLQ を Work Queue へ redrive する",
				"3. 原因確認前の DLQ purge は禁止",
				"4. job_completed ログ・DLQ 空・OK 遷移通知で復旧を確認する",
			},
		}, true
	case alarmNameSchedulerDLQ:
		return alarmGuide{
			whatHappened: "Scheduler DLQ にメッセージが溜まりました",
			actionNeeded: "対応必要",
			steps: []string{
				"1. schedule-checks ログと Scheduler DLQ の message を確認する",
				"2. 原因解消後に対象周期を再実行する",
				"3. 原因確認前の DLQ purge は禁止",
				"4. OK 遷移通知で復旧を確認する",
			},
		}, true
	case alarmNameWorkQueueStall:
		return alarmGuide{
			whatHappened: "Work Queue の最古メッセージが2時間を超えて滞留しています",
			actionNeeded: "対応必要",
			steps: []string{
				"1. check-worker の event source mapping が有効かを確認する",
				"2. check-worker の job_error と throttle を確認する",
				"3. Work Queue の滞留数が増加中なら Scheduler を停止する",
				"4. 滞留が増加していない場合は原因を確認して通常手順へ戻す。purge は通常手順にしない",
			},
		}, true
	default:
		return alarmGuide{}, false
	}
}

// OK 遷移は既知3 Alarm 共通の復旧案内にし種類別手順へ分岐させない（SPECIFICATION.md 17.2.2）。
func buildAlarmMessage(alarm alarmNotificationInput) string {
	var b strings.Builder
	switch alarm.StateValue {
	case AlarmStateOK:
		b.WriteString(fmt.Sprintf("✅ CloudWatch Alarm 復旧: %s\n", alarm.AlarmName))
		b.WriteString("状態: OK（復旧済み）\n")
		b.WriteString("現時点の追加対応は不要です。\n")
		b.WriteString("ただし ALARM 期間の原因調査が必要なケースがある場合は CloudWatch Alarm 詳細と当該時間帯のログを確認してください。\n")
	default:
		guide, known := guideForAlarm(alarm.AlarmName)
		b.WriteString(fmt.Sprintf("🚨 CloudWatch Alarm 発報: %s\n", alarm.AlarmName))
		b.WriteString(fmt.Sprintf("状態: %s\n", alarm.StateValue))
		if known {
			b.WriteString(fmt.Sprintf("何が起きたか: %s\n", guide.whatHappened))
			b.WriteString(fmt.Sprintf("対応: %s\n", guide.actionNeeded))
			b.WriteString("手順:\n")
			for _, step := range guide.steps {
				b.WriteString(step + "\n")
			}
		} else {
			b.WriteString("何が起きたか: 対応判断不能（未知の Alarm 名です）\n")
			b.WriteString("対応: 対応判断不能\n")
			b.WriteString("手順:\n")
			b.WriteString("1. CloudWatch コンソールでこの Alarm の詳細を確認する\n")
			b.WriteString("2. Alarm が参照する metric・対象リソースの当該時間帯のログを確認する\n")
		}
	}
	b.WriteString(fmt.Sprintf("state reason: %s\n", alarmFieldOrUnknown(alarm.Reason)))
	b.WriteString(fmt.Sprintf("状態更新時刻: %s", alarmFieldOrUnknown(alarm.StateTimestamp)))
	return b.String()
}

// payload の field 欠落時は推測値を埋めず固定文言へ fallback する（SPECIFICATION.md 17.2.1）。
func alarmFieldOrUnknown(v string) string {
	if v == "" {
		return unknownFieldText
	}
	return v
}
