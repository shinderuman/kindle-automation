// Package infra は kindle-automation 固有の template.yaml 不変条件を検証する。
// 一般 schema・property 型・!GetAtt 参照整合性は sam validate --lint (cfn-lint) へ委ねる (AGENTS.md §12)。
// この test は cfn-lint 範囲外のプロジェクト固有契約(周期・無効化・BatchSize・禁止リソース・IAM・SSE 等)だけを扱う。
package infra

import (
	"os"
	"strconv"
	"testing"

	"gopkg.in/yaml.v3"
)

// ───────── YAML helpers ─────────

// top は template.yaml の top-level mapping を返す。
func top(t *testing.T) map[string]*yaml.Node {
	t.Helper()
	data, err := os.ReadFile("template.yaml")
	if err != nil {
		t.Fatalf("read template: %v", err)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		t.Fatalf("parse template: %v", err)
	}
	return fields(doc.Content[0])
}

func resources(t *testing.T) map[string]*yaml.Node {
	t.Helper()
	return fields(top(t)["Resources"])
}

// fields は mapping node を key→node へ変換する。非 mapping は空 map。
func fields(n *yaml.Node) map[string]*yaml.Node {
	out := map[string]*yaml.Node{}
	if n == nil || n.Kind != yaml.MappingNode {
		return out
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		out[n.Content[i].Value] = n.Content[i+1]
	}
	return out
}

// props は resource の Properties mapping を返す (fields が nil も処理するため存在チェック不要)。
func props(res map[string]*yaml.Node, name string) map[string]*yaml.Node {
	return fields(fields(res[name])["Properties"])
}

// scalar は mapping から key の scalar 値を返す。未設定・非 scalar は空文字列。
// !GetAtt X.Y 等の組込み関数短縮形も scalar node となり参照文字列が Value に入る。
func scalar(m map[string]*yaml.Node, key string) string {
	n := m[key]
	if n == nil || n.Kind != yaml.ScalarNode {
		return ""
	}
	return n.Value
}

func hasKey(m map[string]*yaml.Node, key string) bool { _, ok := m[key]; return ok }

// ofType は指定 Type の resource 名→Properties を返す。
func ofType(res map[string]*yaml.Node, typ string) map[string]map[string]*yaml.Node {
	out := map[string]map[string]*yaml.Node{}
	for name, r := range res {
		if scalar(fields(r), "Type") == typ {
			out[name] = props(res, name)
		}
	}
	return out
}

// refsCond は !If [Cond, ...] node が condition を参照するかを返す。
func refsCond(n *yaml.Node, cond string) bool {
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

// grantsAction は Role のいずれかの Policy Statement が act を許可するか (Action は scalar/sequence どちらも可)。
func grantsAction(roleProps map[string]*yaml.Node, act string) bool {
	policies := roleProps["Policies"]
	if policies == nil || policies.Kind != yaml.SequenceNode {
		return false
	}
	for _, p := range policies.Content {
		stmts := fields(fields(p)["PolicyDocument"])["Statement"]
		if stmts == nil || stmts.Kind != yaml.SequenceNode {
			continue
		}
		for _, stmt := range stmts.Content {
			a := fields(stmt)["Action"]
			if a == nil {
				continue
			}
			if a.Kind == yaml.ScalarNode && a.Value == act {
				return true
			}
			for _, v := range a.Content {
				if v.Value == act {
					return true
				}
			}
		}
	}
	return false
}

func atoi(t *testing.T, s, what string) int {
	t.Helper()
	n, err := strconv.Atoi(s)
	if err != nil {
		t.Fatalf("%s = %q, want integer", what, s)
	}
	return n
}

// forbiddenTypes は運用負荷と cost を増すため本プロジェクトが含めてはならない resource (AGENTS.md §1/§13)。
var forbiddenTypes = []string{
	"AWS::ApiGateway::RestApi", "AWS::ApiGatewayV2::Api",
	"AWS::EC2::VPC", "AWS::EC2::NatGateway",
	"AWS::ECS::Cluster", "AWS::ECS::TaskDefinition",
	"AWS::StepFunctions::StateMachine", "AWS::DynamoDB::Table",
}

// TestTemplate_ResourceShape は構成・必須 Lambda・禁止リソース・S3 bucket 非管理を検証する (AGENTS.md §1/§13)。
func TestTemplate_ResourceShape(t *testing.T) {
	res := resources(t)
	for typ, want := range map[string]int{
		"AWS::Serverless::Function": 2,
		"AWS::Scheduler::Schedule":  3,
		"AWS::SQS::Queue":           3,
		"AWS::Logs::LogGroup":       2,
		"AWS::Logs::MetricFilter":   2,
		"AWS::CloudWatch::Alarm":    3,
	} {
		if got := len(ofType(res, typ)); got != want {
			t.Errorf("%s count = %d, want %d", typ, got, want)
		}
	}
	for _, name := range []string{"ScheduleChecksFunction", "CheckWorkerFunction"} {
		if res[name] == nil {
			t.Errorf("required Lambda %s missing", name)
		}
	}
	for name, r := range res {
		typ := scalar(fields(r), "Type")
		for _, f := range forbiddenTypes {
			if typ == f {
				t.Errorf("forbidden resource %s present: %s", f, name)
			}
		}
		if typ == "AWS::S3::Bucket" {
			t.Errorf("S3 bucket must not be stack-managed (既存 bucket は削除対象外): %s", name)
		}
	}
}

// TestTemplate_DisableDefaults は Scheduler/worker mapping が default 無効となる Parameter 既定値を検証する (SPECIFICATION.md §20.3)。
// Condition の存在は !If 参照を cfn-lint が検証するため、ここでは既定値 "false" だけを固定する。
func TestTemplate_DisableDefaults(t *testing.T) {
	params := fields(top(t)["Parameters"])
	for _, p := range []string{"SchedulersEnabled", "WorkerMappingEnabled"} {
		if scalar(fields(params[p]), "Default") != "false" {
			t.Errorf("Parameter %s Default must be false (初期deploy無効化)", p)
		}
	}
}

// TestTemplate_SchedulerCycles は 3 Scheduler の業務周期(sale=2h, new_release/paper_to_kindle=1日4回)・JST・default 無効を検証する (SPECIFICATION.md §6)。
// target/retry/DLQ 参照解決・check_type wiring は cfn-lint/review へ委ねる。
func TestTemplate_SchedulerCycles(t *testing.T) {
	res := resources(t)
	want := map[string]string{
		"SaleSchedule":          "cron(0 0/2 * * ? *)",
		"NewReleaseSchedule":    "cron(10 0/6 * * ? *)",
		"PaperToKindleSchedule": "cron(20 0/6 * * ? *)",
	}
	for name, p := range ofType(res, "AWS::Scheduler::Schedule") {
		cron, ok := want[name]
		if !ok {
			t.Errorf("unexpected schedule resource %s", name)
			continue
		}
		if got := scalar(p, "ScheduleExpression"); got != cron {
			t.Errorf("%s ScheduleExpression = %q, want %q", name, got, cron)
		}
		if scalar(p, "ScheduleExpressionTimezone") != "Asia/Tokyo" {
			t.Errorf("%s ScheduleExpressionTimezone must be Asia/Tokyo", name)
		}
		if !refsCond(p["State"], "SchedulersEnabled") {
			t.Errorf("%s State must !If SchedulersEnabled (default false -> DISABLED)", name)
		}
	}
}

// TestTemplate_WorkerMapping は BatchSize=1・partial batch response なし・default 無効を検証する (SPECIFICATION.md §7.4)。
// handler は失敗時に error を返し events.SQSEventResponse は返さないため FunctionResponseTypes を設定しない。
func TestTemplate_WorkerMapping(t *testing.T) {
	res := resources(t)
	workEvt := fields(fields(props(res, "CheckWorkerFunction")["Events"])["WorkQueue"])
	mp := fields(workEvt["Properties"])
	if scalar(mp, "BatchSize") != "1" {
		t.Errorf("CheckWorker BatchSize must be 1 (Amazon 直列化)")
	}
	if hasKey(mp, "FunctionResponseTypes") {
		t.Errorf("CheckWorker FunctionResponseTypes must not be set (handler returns error)")
	}
	if !refsCond(mp["Enabled"], "WorkerMappingEnabled") {
		t.Errorf("CheckWorker event mapping Enabled must !If WorkerMappingEnabled")
	}
}

// TestTemplate_Queues は queue 3本の SSE-SQS・FIFO 区分・visibility >= worker Timeout×6 を検証する (AGENTS.md §13)。
// 保持期間や RedrivePolicy の参照解決は cfn-lint/review へ委ねる。
func TestTemplate_Queues(t *testing.T) {
	res := resources(t)
	for name, q := range ofType(res, "AWS::SQS::Queue") {
		if scalar(q, "SqsManagedSseEnabled") != "true" {
			t.Errorf("%s SqsManagedSseEnabled must be true (SSE-SQS)", name)
		}
	}
	workerTimeout := atoi(t, scalar(props(res, "CheckWorkerFunction"), "Timeout"), "CheckWorkerFunction Timeout")
	work := props(res, "WorkQueue")
	if scalar(work, "FifoQueue") != "true" || scalar(work, "ContentBasedDeduplication") != "false" {
		t.Errorf("WorkQueue must be FIFO with ContentBasedDeduplication=false (job_id SHA-256 を app 指定)")
	}
	if vis := atoi(t, scalar(work, "VisibilityTimeout"), "WorkQueue VisibilityTimeout"); vis < workerTimeout*6 {
		t.Errorf("WorkQueue VisibilityTimeout = %d, want >= %d (worker Timeout %d × 6)", vis, workerTimeout*6, workerTimeout)
	}
	if scalar(props(res, "WorkDLQ"), "FifoQueue") != "true" {
		t.Errorf("WorkDLQ must be FIFO (Work Queue の DLQ も FIFO)")
	}
	if hasKey(props(res, "SchedulerDLQ"), "FifoQueue") {
		t.Errorf("SchedulerDLQ must be Standard (FifoQueue 未設定)")
	}
}

// TestTemplate_LogsAndMetrics は 2 Log Group の保持30日と ErrorCount metric 集計を検証する (SPECIFICATION.md §5.4/§18.2)。
func TestTemplate_LogsAndMetrics(t *testing.T) {
	res := resources(t)
	for name, lg := range ofType(res, "AWS::Logs::LogGroup") {
		if scalar(lg, "RetentionInDays") != "30" {
			t.Errorf("%s RetentionInDays must be 30", name)
		}
	}
	for name, mf := range ofType(res, "AWS::Logs::MetricFilter") {
		if scalar(mf, "FilterPattern") != `{ $.level = "ERROR" }` {
			t.Errorf("%s FilterPattern must aggregate level=ERROR only", name)
		}
		mts := mf["MetricTransformations"]
		if mts == nil || len(mts.Content) == 0 || scalar(fields(mts.Content[0]), "MetricName") != "ErrorCount" {
			t.Errorf("%s must emit single ErrorCount metric", name)
		}
	}
}

// TestTemplate_Alarms は Alarm が ALARM 遷移でのみ schedule-checks を起動する (OKActions なし) ことを検証する (SPECIFICATION.md §17.2)。
func TestTemplate_Alarms(t *testing.T) {
	res := resources(t)
	for name, a := range ofType(res, "AWS::CloudWatch::Alarm") {
		if hasKey(a, "OKActions") {
			t.Errorf("alarm %s must not have OKActions (OK 遷移では起動しない)", name)
		}
		actions := a["AlarmActions"]
		if actions == nil || len(actions.Content) == 0 {
			t.Errorf("alarm %s must have AlarmActions (ALARM 遷移で起動)", name)
		}
	}
}

// TestTemplate_IAM は Lambda 実行 Role の分離と iam:PassRole 不付与を検証する (AGENTS.md §13)。
// SchedulerRole の action 構成・各 Role の詳細 permission は cfn-lint と実装 review へ委ねる。
func TestTemplate_IAM(t *testing.T) {
	res := resources(t)
	schedRole := scalar(props(res, "ScheduleChecksFunction"), "Role")
	workerRole := scalar(props(res, "CheckWorkerFunction"), "Role")
	if schedRole == "" || workerRole == "" {
		t.Fatalf("Lambda Role missing")
	}
	if schedRole == workerRole {
		t.Errorf("schedule-checks と check-worker が同じ Role を共有: %s", schedRole)
	}
	for name, roleProps := range ofType(res, "AWS::IAM::Role") {
		if grantsAction(roleProps, "iam:PassRole") {
			t.Errorf("iam:PassRole must not be granted: %s", name)
		}
	}
}
