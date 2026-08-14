package checkerconfig

import (
	"bytes"
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

func TestApplyMigration_NormalizesEscapeRepresentationsInAllSections(t *testing.T) {
	// 生の&<>で書いた入力を、JSON上で実文字を表す単一backslashのescape表現へ変換する。
	// 二重backslashの\\u0026は文字列データのliteralなので置換対象にしない。
	body := []byte(`{"ReportFailure":true,"SaleChecker":{"Enabled":true,"GistID":"g1","GistFilename":"a&b.md","SaleThreshold":151,"PointPercent":20,"PriceChangeAmount":100,"Memo":"esc & lit \\u0026 esc & mix"},"PaperToKindleChecker":{"Enabled":true,"GistID":"g3","GistFilename":"paper.md","Note":"<&> and \\u003c literal"},"NewReleaseChecker":{"Enabled":false,"GistID":"g2","GistFilename":"new.md","CycleDays":1.0,"Query":"q \\u0026 keep & real"}}`)
	body = bytes.ReplaceAll(body, []byte("&"), []byte(`\u0026`))
	body = bytes.ReplaceAll(body, []byte("<"), []byte(`\u003c`))
	body = bytes.ReplaceAll(body, []byte(">"), []byte(`\u003e`))
	if !bytes.Contains(body, []byte(`\u0026`)) || !bytes.Contains(body, []byte(`\u003c`)) {
		t.Fatal("input must contain single-backslash escape representations")
	}
	out, _, err := applyMigration(body)
	if err != nil {
		t.Fatalf("applyMigration: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("decode migrated: %v\n%s", err, out)
	}
	sale := got["SaleChecker"].(map[string]any)
	if sale["GistFilename"] != "a&b.md" {
		t.Errorf("SaleChecker.GistFilename = %q, want %q", sale["GistFilename"], "a&b.md")
	}
	if want := "esc & lit \\u0026 esc & mix"; sale["Memo"] != want {
		t.Errorf("SaleChecker.Memo = %q, want %q", sale["Memo"], want)
	}
	paper := got["PaperToKindleChecker"].(map[string]any)
	if want := "<&> and \\u003c literal"; paper["Note"] != want {
		t.Errorf("PaperToKindleChecker.Note = %q, want %q", paper["Note"], want)
	}
	nr := got["NewReleaseChecker"].(map[string]any)
	if want := "q \\u0026 keep & real"; nr["Query"] != want {
		t.Errorf("NewReleaseChecker.Query = %q, want %q", nr["Query"], want)
	}
	if nr["MinPrice"].(float64) != 221 {
		t.Errorf("MinPrice = %v, want 221", nr["MinPrice"])
	}
	// literal表現(\\u0026)はescape表現(&)の部分文字列のため、先に除去してから残留を検出する。
	stripped := string(out)
	for _, lit := range []string{`\\u0026`, `\\u003c`, `\\u003e`} {
		stripped = strings.ReplaceAll(stripped, lit, "")
	}
	for _, esc := range []string{"\\u0026", "\\u003c", "\\u003e"} {
		if strings.Contains(stripped, esc) {
			t.Errorf("HTML escape %s must not appear: %s", esc, out)
		}
	}
	if !strings.Contains(string(out), `lit \\u0026 esc`) {
		t.Errorf("literal backslash-u0026 must be re-encoded as data: %s", out)
	}
}

func TestApplyMigration_PreservesNumberLiterals(t *testing.T) {
	body := []byte(`{"SaleChecker":{"Enabled":true,"GistID":"g1","GistFilename":"sale.md","SaleThreshold":151,"PointPercent":20,"PriceChangeAmount":100,"LargeID":9007199254740993},"NewReleaseChecker":{"Enabled":false,"GistID":"g2","GistFilename":"new.md","Ratio":1.0},"PaperToKindleChecker":{"Enabled":true,"GistID":"g3","GistFilename":"paper.md"}}`)
	out, _, err := applyMigration(body)
	if err != nil {
		t.Fatalf("applyMigration: %v", err)
	}
	for _, want := range []string{
		`"LargeID": 9007199254740993`,
		`"Ratio": 1.0`,
	} {
		if !strings.Contains(string(out), want) {
			t.Errorf("number literal %s must be preserved verbatim:\n%s", want, out)
		}
	}
}

func TestApplyMigration_NoTrailingNewline(t *testing.T) {
	body := []byte(`{"SaleChecker":{"Enabled":true,"GistID":"g1","GistFilename":"sale.md","SaleThreshold":151,"PointPercent":20,"PriceChangeAmount":100},"NewReleaseChecker":{"Enabled":false,"GistID":"g2","GistFilename":"new.md"},"PaperToKindleChecker":{"Enabled":true,"GistID":"g3","GistFilename":"paper.md"}}`)
	out, _, err := applyMigration(body)
	if err != nil {
		t.Fatalf("applyMigration: %v", err)
	}
	if bytes.HasSuffix(out, []byte("\n")) {
		t.Errorf("output must not end with newline: %q", out)
	}
	if !bytes.Contains(out, []byte("\n")) {
		t.Errorf("output must contain newlines for indent: %q", out)
	}
}
