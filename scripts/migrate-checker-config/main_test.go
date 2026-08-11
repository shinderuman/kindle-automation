package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/shinderuman/kindle-automation/internal/storage"
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
	body := []byte(`{"ReportFailure":true,"FutureChecker":{"X":1},"SaleChecker":{"Enabled":true,"GistID":"g1","GistFilename":"sale.md","SaleThreshold":151,"PointPercent":20,"PriceChangeAmount":100,"Memo":"keep"}}`)
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
	if sale["Memo"] != "keep" {
		t.Errorf("unknown field within Checker must be preserved")
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

func TestMigrateObject_DryRunDoesNotWrite(t *testing.T) {
	ctx := context.Background()
	body := []byte(`{"ReportFailure":true,"SaleChecker":{"Enabled":true,"GistID":"g1","GistFilename":"sale.md","SaleThreshold":151,"PointPercent":20,"PriceChangeAmount":100},"NewReleaseChecker":{"Enabled":false,"GistID":"g2","GistFilename":"new.md"},"PaperToKindleChecker":{"Enabled":true,"GistID":"g3","GistFilename":"paper.md"}}`)
	store := &applyStore{body: body, etag: "etag-1"}

	if _, err := migrateObject(ctx, store, "checker_configs.json", false); err != nil {
		t.Fatalf("migrateObject dry-run: %v", err)
	}
	if store.putCalls != 0 {
		t.Errorf("dry-run must not Put: %d", store.putCalls)
	}
}

func TestMigrateObject_ApplyWritesWithIfMatch(t *testing.T) {
	ctx := context.Background()
	body := []byte(`{"ReportFailure":true,"SaleChecker":{"Enabled":true,"GistID":"g1","GistFilename":"sale.md","SaleThreshold":151,"PointPercent":20,"PriceChangeAmount":100},"NewReleaseChecker":{"Enabled":false,"GistID":"g2","GistFilename":"new.md"},"PaperToKindleChecker":{"Enabled":true,"GistID":"g3","GistFilename":"paper.md"}}`)
	store := &applyStore{body: body, etag: "etag-1"}

	rep, err := migrateObject(ctx, store, "checker_configs.json", true)
	if err != nil {
		t.Fatalf("migrateObject apply: %v", err)
	}
	if store.putCalls != 1 {
		t.Errorf("putCalls = %d, want 1", store.putCalls)
	}
	if store.putOpts.IfMatch != "etag-1" {
		t.Errorf("IfMatch = %q, want etag-1", store.putOpts.IfMatch)
	}
	if rep.MinPriceAdded != true {
		t.Errorf("MinPriceAdded = %v, want true", rep.MinPriceAdded)
	}
	var got map[string]any
	json.Unmarshal(store.putBody, &got)
	if _, ok := got["ReportFailure"]; ok {
		t.Errorf("ReportFailure must be removed in written body")
	}
	nr := got["NewReleaseChecker"].(map[string]any)
	if nr["MinPrice"].(float64) != 221 {
		t.Errorf("MinPrice = %v, want 221", nr["MinPrice"])
	}
}

func TestMigrateObject_ApplyThenRerunNoChange(t *testing.T) {
	ctx := context.Background()
	body := []byte(`{"ReportFailure":true,"SaleChecker":{"Enabled":true,"GistID":"g1","GistFilename":"sale.md","SaleThreshold":151,"PointPercent":20,"PriceChangeAmount":100},"NewReleaseChecker":{"Enabled":false,"GistID":"g2","GistFilename":"new.md"},"PaperToKindleChecker":{"Enabled":true,"GistID":"g3","GistFilename":"paper.md"}}`)
	store := storage.NewMemStore()
	store.Seed("checker_configs.json", string(body))

	if _, err := migrateObject(ctx, store, "checker_configs.json", true); err != nil {
		t.Fatalf("first apply: %v", err)
	}
	obj, _ := store.Get(ctx, "checker_configs.json")
	rep, err := migrateObject(ctx, store, "checker_configs.json", false)
	if err != nil {
		t.Fatalf("rerun dry-run: %v", err)
	}
	if rep.RemovedTopLevel != 0 || rep.RemovedPerChecker != 0 || rep.MinPriceAdded {
		t.Errorf("rerun must report no change: %+v", rep)
	}
	obj2, _ := store.Get(ctx, "checker_configs.json")
	if string(obj2.Body) != string(obj.Body) {
		t.Errorf("rerun must not change already-migrated body")
	}
}

func TestMigrateObject_NotFound(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemStore()
	_, err := migrateObject(ctx, store, "checker_configs.json", false)
	if err == nil {
		t.Fatal("object 不存在で error がない")
	}
	if !errors.Is(err, storage.ErrObjectNotFound) {
		t.Errorf("err = %v, want ErrObjectNotFound を含む", err)
	}
}

func TestMigrateObject_RejectsInvalidMigratedConfig(t *testing.T) {
	ctx := context.Background()
	body := []byte(`{"NewReleaseChecker":{"Enabled":true,"GistID":"","GistFilename":""}}`)
	store := &applyStore{body: body, etag: "etag-1"}

	_, err := migrateObject(ctx, store, "checker_configs.json", false)
	if err == nil {
		t.Fatal("invalid migrated config must be rejected")
	}
	if !strings.Contains(err.Error(), "validate") {
		t.Errorf("err = %v, want validation failure", err)
	}
	if store.putCalls != 0 {
		t.Errorf("must not Put invalid body: %d", store.putCalls)
	}
}

type applyStore struct {
	body     []byte
	etag     string
	putBody  []byte
	putOpts  storage.PutOptions
	putCalls int
}

func (s *applyStore) Get(_ context.Context, _ string) (storage.Object, error) {
	return storage.Object{Body: s.body, ETag: s.etag}, nil
}

func (s *applyStore) Put(_ context.Context, _ string, body []byte, opts storage.PutOptions) error {
	s.putBody = body
	s.putOpts = opts
	s.putCalls++
	return nil
}
