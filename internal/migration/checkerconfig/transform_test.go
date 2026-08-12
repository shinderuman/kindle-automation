package checkerconfig

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestApplyMigration_RemovesOldFieldsAndAddsMinPrice(t *testing.T) {
	body := []byte(`{
		"ReportFailure": true,
		"SaleChecker": {"Enabled": true, "GistID": "g1", "GistFilename": "sale.md", "SaleThreshold": 151, "PointPercent": 20, "PriceChangeAmount": 100, "ExecutionIntervalMinutes": 5, "GetItemsPaapiRetryCount": 3, "GetItemsInitialRetrySeconds": 2},
		"NewReleaseChecker": {"Enabled": false, "GistID": "g2", "GistFilename": "new.md", "CycleDays": 1.0, "SearchItemsPaapiRetryCount": 3, "SearchItemsInitialRetrySeconds": 2, "GetItemsPaapiRetryCount": 3, "GetItemsInitialRetrySeconds": 2},
		"PaperToKindleChecker": {"Enabled": true, "GistID": "g3", "GistFilename": "paper.md", "CycleDays": 1.0, "SearchItemsPaapiRetryCount": 3, "SearchItemsInitialRetrySeconds": 2, "GetItemsPaapiRetryCount": 3, "GetItemsInitialRetrySeconds": 2}
	}`)
	out, rep, err := applyMigration(body)
	if err != nil {
		t.Fatalf("applyMigration: %v", err)
	}
	if rep.RemovedTopLevel != 1 {
		t.Errorf("RemovedTopLevel = %d, want 1", rep.RemovedTopLevel)
	}
	if rep.RemovedPerChecker != 13 {
		t.Errorf("RemovedPerChecker = %d, want 13", rep.RemovedPerChecker)
	}
	if !rep.MinPriceAdded {
		t.Errorf("MinPriceAdded = false, want true")
	}

	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("decode migrated: %v", err)
	}
	if _, ok := got["ReportFailure"]; ok {
		t.Errorf("ReportFailure must be removed")
	}
	sale := got["SaleChecker"].(map[string]any)
	for _, f := range []string{"ExecutionIntervalMinutes", "GetItemsPaapiRetryCount", "GetItemsInitialRetrySeconds"} {
		if _, ok := sale[f]; ok {
			t.Errorf("SaleChecker.%s must be removed", f)
		}
	}
	nr := got["NewReleaseChecker"].(map[string]any)
	for _, f := range []string{"CycleDays", "SearchItemsPaapiRetryCount", "SearchItemsInitialRetrySeconds", "GetItemsPaapiRetryCount", "GetItemsInitialRetrySeconds"} {
		if _, ok := nr[f]; ok {
			t.Errorf("NewReleaseChecker.%s must be removed", f)
		}
	}
	if nr["MinPrice"].(float64) != 221 {
		t.Errorf("MinPrice = %v, want 221", nr["MinPrice"])
	}
	paper := got["PaperToKindleChecker"].(map[string]any)
	for _, f := range []string{"CycleDays", "SearchItemsPaapiRetryCount", "SearchItemsInitialRetrySeconds", "GetItemsPaapiRetryCount", "GetItemsInitialRetrySeconds"} {
		if _, ok := paper[f]; ok {
			t.Errorf("PaperToKindleChecker.%s must be removed", f)
		}
	}
	if sale["SaleThreshold"].(float64) != 151 {
		t.Errorf("SaleThreshold not preserved: %v", sale["SaleThreshold"])
	}
}

func TestApplyMigration_PreservesMinPriceWhenPresent(t *testing.T) {
	body := []byte(`{"SaleChecker":{"Enabled":true,"GistID":"g1","GistFilename":"sale.md","SaleThreshold":151,"PointPercent":20,"PriceChangeAmount":100},"NewReleaseChecker":{"Enabled":false,"GistID":"g2","GistFilename":"new.md","MinPrice":500},"PaperToKindleChecker":{"Enabled":true,"GistID":"g3","GistFilename":"paper.md"}}`)
	out, rep, err := applyMigration(body)
	if err != nil {
		t.Fatalf("applyMigration: %v", err)
	}
	if rep.MinPriceAdded {
		t.Errorf("MinPriceAdded = true, want false (already present)")
	}
	var got map[string]any
	json.Unmarshal(out, &got)
	nr := got["NewReleaseChecker"].(map[string]any)
	if nr["MinPrice"].(float64) != 500 {
		t.Errorf("MinPrice = %v, want 500 (not overwritten)", nr["MinPrice"])
	}
}

func TestApplyMigration_PreservesUnknownFieldsAndSections(t *testing.T) {
	body := []byte(`{"ReportFailure":true,"FutureChecker":{"X":1},"SaleChecker":{"Enabled":true,"GistID":"g1","GistFilename":"sale.md","SaleThreshold":151,"PointPercent":20,"PriceChangeAmount":100,"Memo":"keep&a=b"}}`)
	out, _, err := applyMigration(body)
	if err != nil {
		t.Fatalf("applyMigration: %v", err)
	}
	var got map[string]any
	json.Unmarshal(out, &got)
	if _, ok := got["FutureChecker"]; !ok {
		t.Errorf("unknown top-level section must be preserved")
	}
	sale := got["SaleChecker"].(map[string]any)
	if sale["Memo"] != "keep&a=b" {
		t.Errorf("unknown field within Checker must be preserved")
	}
	if strings.Contains(string(out), `\u0026`) {
		t.Errorf("ampersand must not be HTML-escaped: %s", out)
	}
}

func TestApplyMigration_UsesFourSpaceIndent(t *testing.T) {
	body := []byte(`{"SaleChecker":{"Enabled":true,"GistID":"g1","GistFilename":"sale.md","SaleThreshold":151,"PointPercent":20,"PriceChangeAmount":100},"NewReleaseChecker":{"Enabled":false,"GistID":"g2","GistFilename":"new.md"},"PaperToKindleChecker":{"Enabled":true,"GistID":"g3","GistFilename":"paper.md"}}`)
	out, _, err := applyMigration(body)
	if err != nil {
		t.Fatalf("applyMigration: %v", err)
	}
	for _, want := range []string{
		"\n    \"NewReleaseChecker\": {",
		"\n        \"MinPrice\": 221",
		"\n    \"PaperToKindleChecker\": {",
	} {
		if !strings.Contains(string(out), want) {
			t.Errorf("4-space indented output missing %q:\n%s", want, out)
		}
	}
}

func TestApplyMigration_Idempotent(t *testing.T) {
	body := []byte(`{"ReportFailure":true,"SaleChecker":{"Enabled":true,"GistID":"g1","GistFilename":"sale.md","SaleThreshold":151,"PointPercent":20,"PriceChangeAmount":100,"ExecutionIntervalMinutes":5},"NewReleaseChecker":{"Enabled":false,"GistID":"g2","GistFilename":"new.md","CycleDays":1.0},"PaperToKindleChecker":{"Enabled":true,"GistID":"g3","GistFilename":"paper.md"}}`)
	first, _, err := applyMigration(body)
	if err != nil {
		t.Fatalf("first applyMigration: %v", err)
	}
	second, rep, err := applyMigration(first)
	if err != nil {
		t.Fatalf("second applyMigration: %v", err)
	}
	if rep.RemovedTopLevel != 0 || rep.RemovedPerChecker != 0 || rep.MinPriceAdded {
		t.Errorf("rerun must be no-op: %+v", rep)
	}
	if string(second) != string(first) {
		t.Errorf("rerun changed body: first=%s second=%s", first, second)
	}
}

func TestApplyMigration_InvalidJSON(t *testing.T) {
	if _, _, err := applyMigration([]byte(`{`)); err == nil {
		t.Fatal("want decode error for invalid JSON")
	}
}
