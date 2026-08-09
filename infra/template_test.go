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
