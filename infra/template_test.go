// Package infra は CloudFormation/SAM テンプレートの構造検証を行う。
package infra

import (
	"os"
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
