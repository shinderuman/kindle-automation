// Package infra は CloudFormation/SAM テンプレートの構造検証を行う。
package infra

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// mappingFields は mapping node を key→node の map へ変換する。
// 短縮形組込み関数（!GetAtt 等）は未知 tag となるため値の解釈は行わず構造だけを見る。
func mappingFields(n *yaml.Node) map[string]*yaml.Node {
	out := map[string]*yaml.Node{}
	if n == nil || n.Kind != yaml.MappingNode {
		return out
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		out[n.Content[i].Value] = n.Content[i+1]
	}
	return out
}

// seqItems は sequence node の要素を返す。
func seqItems(n *yaml.Node) []*yaml.Node {
	if n == nil || n.Kind != yaml.SequenceNode {
		return nil
	}
	return n.Content
}

// scalarValue は mapping から key の scalar 文字列値を返す。未設定・非 scalar は空。
// !GetAtt X 等の組込み関数短縮形は scalar node となり Value へ対象参照文字列が入る。
func scalarValue(m map[string]*yaml.Node, key string) string {
	n := m[key]
	if n == nil || n.Kind != yaml.ScalarNode {
		return ""
	}
	return n.Value
}

// resourcesByType は指定 Type の resource 名→node 一覧を返す。
func resourcesByType(resources map[string]*yaml.Node, typeStr string) map[string]*yaml.Node {
	out := map[string]*yaml.Node{}
	for name, res := range resources {
		if scalarValue(mappingFields(res), "Type") == typeStr {
			out[name] = res
		}
	}
	return out
}

// nodeValues は scalar または sequence node から文字列値一覧を返す。
// IAM Policy の Action/Resource が単値（scalar）と配列（sequence）どちらでも扱うため。
func nodeValues(n *yaml.Node) []string {
	if n == nil {
		return nil
	}
	if n.Kind == yaml.ScalarNode {
		return []string{n.Value}
	}
	out := make([]string, 0, len(n.Content))
	for _, c := range seqItems(n) {
		out = append(out, c.Value)
	}
	return out
}

// nodeReferencesCondition は !If [Cond, ...] node が指定 condition 名を参照するかを返す。
// SAM Condition への参照だけで値の解決は行わない（parameter default 検査と組み合わせて契約を固定）。
func nodeReferencesCondition(n *yaml.Node, cond string) bool {
	if n == nil || n.Tag != "!If" {
		return false
	}
	for _, c := range n.Content {
		if c.Value == cond {
			return true
		}
	}
	return false
}

// parseTemplate は template.yaml を読み込み Resources mapping を返す。
func parseTemplate(t *testing.T) map[string]*yaml.Node {
	t.Helper()
	data, err := os.ReadFile("template.yaml")
	if err != nil {
		t.Fatalf("read template: %v", err)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		t.Fatalf("parse template: %v", err)
	}
	return mappingFields(mappingFields(doc.Content[0])["Resources"])
}

// TestTemplate_AlarmsNotifyOnlyOnAlarm は CloudWatch Alarm 3件が AlarmActions のみを持ち
// OKActions を持たないことを検証する（SPECIFICATION.md 5.1/17.2）。
// OK 遷移で schedule-checks を起動しないことで Slack 発報通知を防ぐ。
func TestTemplate_AlarmsNotifyOnlyOnAlarm(t *testing.T) {
	data, err := os.ReadFile("template.yaml")
	if err != nil {
		t.Fatalf("read template: %v", err)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		t.Fatalf("parse template: %v", err)
	}
	top := mappingFields(doc.Content[0])
	resources := mappingFields(top["Resources"])

	alarmCount := 0
	for name, res := range resources {
		rf := mappingFields(res)
		typeNode, ok := rf["Type"]
		if !ok || typeNode.Value != "AWS::CloudWatch::Alarm" {
			continue
		}
		alarmCount++
		props := mappingFields(rf["Properties"])
		if _, ok := props["OKActions"]; ok {
			t.Errorf("alarm %s must not have OKActions (OK 遷移では schedule-checks を起動しない)", name)
		}
		if actions := props["AlarmActions"]; len(seqItems(actions)) == 0 {
			t.Errorf("alarm %s must have AlarmActions", name)
		}
	}
	if alarmCount != 3 {
		t.Errorf("CloudWatch::Alarm count = %d, want 3", alarmCount)
	}
}

// TestTemplate_AlarmPermissionPrincipal は CloudWatch Alarm 直接 invoke 用の Lambda Permission が
// 正しい service principal（lambda.alarms.cloudwatch.amazonaws.com）と SourceAccount/SourceArn を持つことを検証する。
// cloudwatch.amazonaws.com では AlarmActions 直接 invoke を認めない（AWS 公式）。
func TestTemplate_AlarmPermissionPrincipal(t *testing.T) {
	data, err := os.ReadFile("template.yaml")
	if err != nil {
		t.Fatalf("read template: %v", err)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		t.Fatalf("parse template: %v", err)
	}
	top := mappingFields(doc.Content[0])
	resources := mappingFields(top["Resources"])

	res, ok := resources["ScheduleChecksAlarmPermission"]
	if !ok {
		t.Fatal("ScheduleChecksAlarmPermission resource missing")
	}
	props := mappingFields(mappingFields(res)["Properties"])
	if principal := props["Principal"]; principal == nil || principal.Value != "lambda.alarms.cloudwatch.amazonaws.com" {
		got := ""
		if principal != nil {
			got = principal.Value
		}
		t.Errorf("Principal = %q, want lambda.alarms.cloudwatch.amazonaws.com", got)
	}
	if action := props["Action"]; action == nil || action.Value != "lambda:InvokeFunction" {
		t.Errorf("Action must be lambda:InvokeFunction")
	}
	if _, ok := props["SourceAccount"]; !ok {
		t.Errorf("SourceAccount must be set for confused-deputy protection (AWS recommendation)")
	}
	if _, ok := props["SourceArn"]; !ok {
		t.Errorf("SourceArn must be set for confused-deputy protection")
	}
}

// TestTemplate_CheckWorkerMappingRedeliverOnError は check-worker の SQS event source mapping が
// BatchSize=1 であり partial batch response（FunctionResponseTypes）を持たないことを検証する。
// handler は events.SQSEventResponse を返さず、失敗時に error を返して SQS へ再配信させる契約（SPECIFICATION.md 7.4）。
// ReportBatchItemFailures を誤って再設定しないよう固定する回帰テスト。
func TestTemplate_CheckWorkerMappingRedeliverOnError(t *testing.T) {
	data, err := os.ReadFile("template.yaml")
	if err != nil {
		t.Fatalf("read template: %v", err)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		t.Fatalf("parse template: %v", err)
	}
	top := mappingFields(doc.Content[0])
	resources := mappingFields(top["Resources"])

	worker, ok := resources["CheckWorkerFunction"]
	if !ok {
		t.Fatal("CheckWorkerFunction resource missing")
	}
	workerProps := mappingFields(mappingFields(worker)["Properties"])
	eventsNode, ok := workerProps["Events"]
	if !ok {
		t.Fatal("CheckWorkerFunction Events missing")
	}
	mapping, ok := mappingFields(eventsNode)["WorkQueue"]
	if !ok {
		t.Fatal("CheckWorkerFunction WorkQueue event missing")
	}
	mappingProps := mappingFields(mappingFields(mapping)["Properties"])

	bs, ok := mappingProps["BatchSize"]
	if !ok {
		t.Fatal("CheckWorkerFunction WorkQueue BatchSize missing")
	}
	if bs.Value != "1" {
		t.Errorf("BatchSize = %q, want 1", bs.Value)
	}
	if frt, ok := mappingProps["FunctionResponseTypes"]; ok {
		t.Errorf("FunctionResponseTypes must not be set (handler returns error, not SQSEventResponse); got %v", seqItems(frt))
	}
}

// TestTemplate_SchedulesMatchCycleSpec は 3 つの EventBridge Scheduler が
// SPECIFICATION.md 6 の周期・timezone・Flexible Time Window・retry・DLQ・target と一致することを検証する。
// セール2時間ごと・新刊1日4回・紙Kindle 1日4回。State は SchedulersEnabled condition 参照（default false=DISABLED）。
func TestTemplate_SchedulesMatchCycleSpec(t *testing.T) {
	resources := parseTemplate(t)

	want := map[string]struct {
		cron      string
		checkType string
	}{
		"SaleSchedule":          {cron: "cron(0 0/2 * * ? *)", checkType: "sale"},
		"NewReleaseSchedule":    {cron: "cron(10 0/6 * * ? *)", checkType: "new_release"},
		"PaperToKindleSchedule": {cron: "cron(20 0/6 * * ? *)", checkType: "paper_to_kindle"},
	}

	gotCount := 0
	for name, res := range resourcesByType(resources, "AWS::Scheduler::Schedule") {
		gotCount++
		spec, ok := want[name]
		if !ok {
			t.Errorf("unexpected schedule resource %s", name)
			continue
		}
		props := mappingFields(mappingFields(res)["Properties"])
		if got := scalarValue(props, "ScheduleExpression"); got != spec.cron {
			t.Errorf("%s ScheduleExpression = %q, want %q", name, got, spec.cron)
		}
		if got := scalarValue(props, "ScheduleExpressionTimezone"); got != "Asia/Tokyo" {
			t.Errorf("%s ScheduleExpressionTimezone = %q, want Asia/Tokyo", name, got)
		}
		if got := scalarValue(mappingFields(props["FlexibleTimeWindow"]), "Mode"); got != "OFF" {
			t.Errorf("%s FlexibleTimeWindow.Mode = %q, want OFF", name, got)
		}
		if !nodeReferencesCondition(props["State"], "SchedulersEnabled") {
			t.Errorf("%s State must reference SchedulersEnabled condition (default false -> DISABLED)", name)
		}
		target := mappingFields(props["Target"])
		if arn := target["Arn"]; arn == nil || arn.Value != "ScheduleChecksFunction.Arn" {
			t.Errorf("%s Target.Arn must !GetAtt ScheduleChecksFunction.Arn", name)
		}
		if role := target["RoleArn"]; role == nil || role.Value != "SchedulerRole.Arn" {
			t.Errorf("%s Target.RoleArn must !GetAtt SchedulerRole.Arn", name)
		}
		// RetryPolicy・DeadLetterConfig・Input は Target 配下（AWS::Scheduler::Schedule の schema）。
		retry := mappingFields(target["RetryPolicy"])
		if got := scalarValue(retry, "MaximumRetryAttempts"); got != "3" {
			t.Errorf("%s Target.RetryPolicy.MaximumRetryAttempts = %q, want 3", name, got)
		}
		if got := scalarValue(retry, "MaximumEventAgeInSeconds"); got != "240" {
			t.Errorf("%s Target.RetryPolicy.MaximumEventAgeInSeconds = %q, want 240", name, got)
		}
		input := scalarValue(target, "Input")
		if !strings.Contains(input, fmt.Sprintf(`"check_type": "%s"`, spec.checkType)) {
			t.Errorf("%s Target.Input must contain check_type %q", name, spec.checkType)
		}
		dlc := mappingFields(target["DeadLetterConfig"])
		if arn := dlc["Arn"]; arn == nil || arn.Value != "SchedulerDLQ.Arn" {
			t.Errorf("%s Target.DeadLetterConfig.Arn must !GetAtt SchedulerDLQ.Arn", name)
		}
	}
	if gotCount != 3 {
		t.Errorf("AWS::Scheduler::Schedule count = %d, want 3", gotCount)
	}
}

// TestTemplate_SQSQueuesContract は Work Queue（FIFO）・Work DLQ（FIFO）・Scheduler DLQ（Standard）が
// SPECIFICATION.md 7.1/6 の FIFO・SSE-SQS・visibility timeout・retention・redrive 設定を満たすことを検証する。
// visibility 180秒は worker timeout 30秒の6倍（AGENTS.md 13）。partial batch response は別テストで担保済み。
func TestTemplate_SQSQueuesContract(t *testing.T) {
	resources := parseTemplate(t)
	queues := resourcesByType(resources, "AWS::SQS::Queue")
	if len(queues) != 3 {
		t.Fatalf("AWS::SQS::Queue count = %d, want 3 (Work Queue, Work DLQ, Scheduler DLQ)", len(queues))
	}

	// Work Queue（FIFO）。amazon-requests 直列化のキュー本体。
	work := mappingFields(mappingFields(queues["WorkQueue"])["Properties"])
	if scalarValue(work, "FifoQueue") != "true" {
		t.Errorf("WorkQueue FifoQueue must be true")
	}
	if scalarValue(work, "ContentBasedDeduplication") != "false" {
		t.Errorf("WorkQueue ContentBasedDeduplication must be false (job_id の SHA-256 を application 側で指定)")
	}
	if scalarValue(work, "VisibilityTimeout") != "180" {
		t.Errorf("WorkQueue VisibilityTimeout = %q, want 180 (worker timeout 30s の6倍)", scalarValue(work, "VisibilityTimeout"))
	}
	if scalarValue(work, "MessageRetentionPeriod") != "345600" {
		t.Errorf("WorkQueue MessageRetentionPeriod = %q, want 345600 (4日)", scalarValue(work, "MessageRetentionPeriod"))
	}
	if scalarValue(work, "SqsManagedSseEnabled") != "true" {
		t.Errorf("WorkQueue SqsManagedSseEnabled must be true (SSE-SQS)")
	}
	rd := mappingFields(work["RedrivePolicy"])
	if scalarValue(rd, "maxReceiveCount") != "5" {
		t.Errorf("WorkQueue RedrivePolicy.maxReceiveCount = %q, want 5", scalarValue(rd, "maxReceiveCount"))
	}
	if arn := rd["deadLetterTargetArn"]; arn == nil || arn.Value != "WorkDLQ.Arn" {
		t.Errorf("WorkQueue RedrivePolicy.deadLetterTargetArn must !GetAtt WorkDLQ.Arn")
	}

	// Work DLQ（FIFO）。
	workDlq := mappingFields(mappingFields(queues["WorkDLQ"])["Properties"])
	if scalarValue(workDlq, "FifoQueue") != "true" {
		t.Errorf("WorkDLQ FifoQueue must be true (Work Queue の DLQ も FIFO)")
	}
	if scalarValue(workDlq, "MessageRetentionPeriod") != "1209600" {
		t.Errorf("WorkDLQ MessageRetentionPeriod = %q, want 1209600 (14日)", scalarValue(workDlq, "MessageRetentionPeriod"))
	}
	if scalarValue(workDlq, "SqsManagedSseEnabled") != "true" {
		t.Errorf("WorkDLQ SqsManagedSseEnabled must be true")
	}

	// Scheduler DLQ（Standard）。
	schedDlq := mappingFields(mappingFields(queues["SchedulerDLQ"])["Properties"])
	if _, ok := schedDlq["FifoQueue"]; ok {
		t.Errorf("SchedulerDLQ must be Standard (FifoQueue 未設定)")
	}
	if scalarValue(schedDlq, "MessageRetentionPeriod") != "1209600" {
		t.Errorf("SchedulerDLQ MessageRetentionPeriod = %q, want 1209600 (14日)", scalarValue(schedDlq, "MessageRetentionPeriod"))
	}
	if scalarValue(schedDlq, "SqsManagedSseEnabled") != "true" {
		t.Errorf("SchedulerDLQ SqsManagedSseEnabled must be true")
	}
}

// TestTemplate_DisableDefaults は SchedulersEnabled/WorkerMappingEnabled が default "false" であり
// 対応 Conditions と event source mapping の Enabled がその条件を参照することを検証する。
// 初期 deploy/cutover 前に Scheduler と event source mapping が無効状態で作られる契約（SPECIFICATION.md 20.3）。
func TestTemplate_DisableDefaults(t *testing.T) {
	data, err := os.ReadFile("template.yaml")
	if err != nil {
		t.Fatalf("read template: %v", err)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		t.Fatalf("parse template: %v", err)
	}
	top := mappingFields(doc.Content[0])

	params := mappingFields(top["Parameters"])
	for _, p := range []string{"SchedulersEnabled", "WorkerMappingEnabled"} {
		pn := params[p]
		if pn == nil {
			t.Errorf("Parameter %s missing", p)
			continue
		}
		if scalarValue(mappingFields(pn), "Default") != "false" {
			t.Errorf("Parameter %s Default must be false (初期deploy無効化)", p)
		}
	}
	conds := mappingFields(top["Conditions"])
	for _, c := range []string{"SchedulersEnabled", "WorkerMappingEnabled"} {
		if conds[c] == nil {
			t.Errorf("Condition %s missing", c)
		}
	}

	// check-worker event source mapping の Enabled は WorkerMappingEnabled condition 参照。
	resources := mappingFields(top["Resources"])
	workerProps := mappingFields(mappingFields(resources["CheckWorkerFunction"])["Properties"])
	mappingProps := mappingFields(mappingFields(mappingFields(workerProps["Events"])["WorkQueue"])["Properties"])
	if !nodeReferencesCondition(mappingProps["Enabled"], "WorkerMappingEnabled") {
		t.Errorf("CheckWorker WorkQueue event source mapping Enabled must reference WorkerMappingEnabled condition")
	}
}

// TestTemplate_LogGroupsAndMetricFilters は 2 Log Group の保持30日と各 ErrorCount metric filter の
// pattern/namespace を検証する（SPECIFICATION.md 5.4/18.2）。PutMetricData は別途コード検査で未使用を担保。
func TestTemplate_LogGroupsAndMetricFilters(t *testing.T) {
	resources := parseTemplate(t)

	logGroups := resourcesByType(resources, "AWS::Logs::LogGroup")
	if len(logGroups) != 2 {
		t.Errorf("AWS::Logs::LogGroup count = %d, want 2", len(logGroups))
	}
	for name, res := range logGroups {
		if scalarValue(mappingFields(mappingFields(res)["Properties"]), "RetentionInDays") != "30" {
			t.Errorf("%s RetentionInDays must be 30", name)
		}
	}

	filters := resourcesByType(resources, "AWS::Logs::MetricFilter")
	if len(filters) != 2 {
		t.Errorf("AWS::Logs::MetricFilter count = %d, want 2 (各 Log Group の ERROR 集計)", len(filters))
	}
	for name, res := range filters {
		props := mappingFields(mappingFields(res)["Properties"])
		if got := scalarValue(props, "FilterPattern"); got != `{ $.level = "ERROR" }` {
			t.Errorf("%s FilterPattern = %q, want level=ERROR のみ", name, got)
		}
		mts := seqItems(props["MetricTransformations"])
		if len(mts) != 1 {
			t.Errorf("%s MetricTransformations count = %d, want 1", name, len(mts))
			continue
		}
		mt := mappingFields(mts[0])
		if scalarValue(mt, "MetricNamespace") != "KindleAutomation" {
			t.Errorf("%s MetricNamespace must be KindleAutomation", name)
		}
		if scalarValue(mt, "MetricName") != "ErrorCount" {
			t.Errorf("%s MetricName must be ErrorCount", name)
		}
	}
}

// TestTemplate_IAMAndScope は IAM/resource scope の不変条件を検証する。
//   - schedule-checks と check-worker が異なる実行 Role（AGENTS.md 13）
//   - iam:PassRole をどこにも与えない（SchedulerRole は CloudFormation が設定、PassRole 不要）
//   - SchedulerRole は lambda:InvokeFunction と SchedulerDLQ 送信だけ
//   - 既存 S3 bucket を stack 管理対象にしない（SPECIFICATION.md 9.1）
//   - API Gateway/VPC/Fargate/Step Functions/DynamoDB を含まない（SPECIFICATION.md 4）
func TestTemplate_IAMAndScope(t *testing.T) {
	resources := parseTemplate(t)

	forbidden := []string{
		"AWS::ApiGateway::RestApi", "AWS::ApiGatewayV2::Api",
		"AWS::EC2::VPC", "AWS::ECS::Cluster", "AWS::ECS::TaskDefinition",
		"AWS::StepFunctions::StateMachine", "AWS::DynamoDB::Table",
	}
	for name, res := range resources {
		rtype := scalarValue(mappingFields(res), "Type")
		for _, f := range forbidden {
			if rtype == f {
				t.Errorf("forbidden resource type %s present: %s (SPECIFICATION.md 4)", f, name)
			}
		}
		if rtype == "AWS::S3::Bucket" {
			t.Errorf("S3 bucket must not be stack-managed (既存 bucket は削除対象外): %s", name)
		}
	}

	// 2 Lambda で異なる実行 Role。
	fnRole := scalarValue(mappingFields(mappingFields(resources["ScheduleChecksFunction"])["Properties"]), "Role")
	workerRole := scalarValue(mappingFields(mappingFields(resources["CheckWorkerFunction"])["Properties"]), "Role")
	if fnRole == "" || workerRole == "" {
		t.Fatalf("Lambda Role missing")
	}
	if fnRole == workerRole {
		t.Errorf("schedule-checks と check-worker が同じ Role を参照: %s", fnRole)
	}

	// IAM Role 内で iam:PassRole を与えない。
	for name, res := range resourcesByType(resources, "AWS::IAM::Role") {
		props := mappingFields(mappingFields(res)["Properties"])
		for _, p := range seqItems(props["Policies"]) {
			doc := mappingFields(mappingFields(p)["PolicyDocument"])
			for _, stmt := range seqItems(doc["Statement"]) {
				for _, act := range nodeValues(mappingFields(stmt)["Action"]) {
					if act == "iam:PassRole" {
						t.Errorf("iam:PassRole must not be granted: %s", name)
					}
				}
			}
		}
	}

	// SchedulerRole は lambda:InvokeFunction と sqs:SendMessage だけ。
	for _, stmt := range policyStatements(resources["SchedulerRole"]) {
		for _, act := range nodeValues(mappingFields(stmt)["Action"]) {
			if act != "lambda:InvokeFunction" && act != "sqs:SendMessage" {
				t.Errorf("SchedulerRole must grant only lambda:InvokeFunction/sqs:SendMessage, got %s", act)
			}
		}
	}
}

// policyStatements は IAM Role の全 managed policy document の Statement 一覧を返す。
func policyStatements(roleNode *yaml.Node) []*yaml.Node {
	props := mappingFields(roleNode)["Properties"]
	var out []*yaml.Node
	for _, p := range seqItems(mappingFields(props)["Policies"]) {
		doc := mappingFields(mappingFields(p)["PolicyDocument"])
		out = append(out, seqItems(doc["Statement"])...)
	}
	return out
}
