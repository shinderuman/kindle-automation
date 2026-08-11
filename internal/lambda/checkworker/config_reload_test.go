package checkworker

import (
	"context"
	"testing"

	"github.com/aws/aws-lambda-go/lambdacontext"

	"github.com/shinderuman/kindle-automation/internal/storage"
)

func TestRefreshVariableConfig_ReloadsThresholdsKeywordsAndGist(t *testing.T) {
	store := storage.NewMemStore()
	store.Seed("checker_configs.json", `{"SaleChecker":{"Enabled":true,"GistID":"g1","GistFilename":"sale.md","SaleThreshold":100,"PointPercent":10,"PriceChangeAmount":50},"NewReleaseChecker":{"Enabled":true,"GistID":"gn","GistFilename":"new.md","MinPrice":221},"PaperToKindleChecker":{"Enabled":true,"GistID":"gp","GistFilename":"paper.md"}}`)
	store.Seed("excluded_title_keywords.json", `["ボックス","セット"]`)
	w := &Worker{
		store:                    store,
		checkerConfigKey:         "checker_configs.json",
		excludedTitleKeywordsKey: "excluded_title_keywords.json",
	}

	if err := w.refreshVariableConfig(context.Background()); err != nil {
		t.Fatalf("first refresh: %v", err)
	}
	if got := w.SaleDeps.Config.Thresholds.SaleThreshold; got != 100 {
		t.Errorf("SaleThreshold = %v, want 100", got)
	}
	if got := w.SaleDeps.Config.Thresholds.PointPercent; got != 10 {
		t.Errorf("PointPercent = %v, want 10", got)
	}
	if got := w.SaleDeps.Config.Thresholds.PriceChangeAmount; got != 50 {
		t.Errorf("PriceChangeAmount = %v, want 50", got)
	}
	if len(w.NRDeps.Config.ExcludedKeywords) != 2 {
		t.Errorf("ExcludedKeywords = %v, want 2 items", w.NRDeps.Config.ExcludedKeywords)
	}
	if w.NRDeps.Config.MinPrice != 221 {
		t.Errorf("MinPrice = %v, want 221", w.NRDeps.Config.MinPrice)
	}
	if w.GistDeps.Settings.Sale.ID != "g1" || w.GistDeps.Settings.NewRelease.ID != "gn" || w.GistDeps.Settings.PaperToKindle.ID != "gp" {
		t.Errorf("Gist settings not mapped: %+v", w.GistDeps.Settings)
	}

	store.Seed("checker_configs.json", `{"SaleChecker":{"Enabled":true,"GistID":"g2","GistFilename":"sale.md","SaleThreshold":200,"PointPercent":20,"PriceChangeAmount":80},"NewReleaseChecker":{"Enabled":true,"GistID":"gn2","GistFilename":"new.md","MinPrice":300},"PaperToKindleChecker":{"Enabled":true,"GistID":"gp2","GistFilename":"paper.md"}}`)
	store.Seed("excluded_title_keywords.json", `["完結"]`)

	if err := w.refreshVariableConfig(context.Background()); err != nil {
		t.Fatalf("second refresh: %v", err)
	}
	if got := w.SaleDeps.Config.Thresholds.SaleThreshold; got != 200 {
		t.Errorf("SaleThreshold after reload = %v, want 200 (warm reload)", got)
	}
	if got := w.SaleDeps.Config.Thresholds.PointPercent; got != 20 {
		t.Errorf("PointPercent after reload = %v, want 20", got)
	}
	if got := w.SaleDeps.Config.Thresholds.PriceChangeAmount; got != 80 {
		t.Errorf("PriceChangeAmount after reload = %v, want 80", got)
	}
	if len(w.NRDeps.Config.ExcludedKeywords) != 1 || w.NRDeps.Config.ExcludedKeywords[0] != "完結" {
		t.Errorf("ExcludedKeywords after reload = %v, want [完結]", w.NRDeps.Config.ExcludedKeywords)
	}
	if w.NRDeps.Config.MinPrice != 300 {
		t.Errorf("MinPrice after reload = %v, want 300 (warm reload)", w.NRDeps.Config.MinPrice)
	}
	if w.GistDeps.Settings.Sale.ID != "g2" {
		t.Errorf("Gist Sale ID after reload = %v, want g2 (warm reload)", w.GistDeps.Settings.Sale.ID)
	}
}

func TestRefreshVariableConfig_RejectsInvalidCheckerConfig(t *testing.T) {
	store := storage.NewMemStore()
	store.Seed("checker_configs.json", `{"SaleChecker":{"Enabled":true,"GistID":"g","GistFilename":"sale.md","SaleThreshold":0,"PointPercent":10,"PriceChangeAmount":50}}`)
	store.Seed("excluded_title_keywords.json", "[]")
	w := &Worker{
		store:                    store,
		checkerConfigKey:         "checker_configs.json",
		excludedTitleKeywordsKey: "excluded_title_keywords.json",
	}
	if err := w.refreshVariableConfig(context.Background()); err == nil {
		t.Fatal("refreshVariableConfig should reject invalid checker config (threshold 0)")
	}
}

func TestRefreshVariableConfig_ExcludedKeywordsLoadFailure(t *testing.T) {
	store := storage.NewMemStore()
	store.Seed("checker_configs.json", checkerConfigWithSaleGistID("g1"))
	store.Seed("excluded_title_keywords.json", "{not-json-array")
	w := &Worker{
		store:                    store,
		checkerConfigKey:         "checker_configs.json",
		excludedTitleKeywordsKey: "excluded_title_keywords.json",
	}
	if err := w.refreshVariableConfig(context.Background()); err == nil {
		t.Fatal("refreshVariableConfig should fail when excluded keywords are undecodable")
	}
}

func TestRefreshVariableConfig_ExcludedKeywordsMissingFails(t *testing.T) {
	store := storage.NewMemStore()
	store.Seed("checker_configs.json", checkerConfigWithSaleGistID("g1"))
	// excluded_title_keywords.json は seed しない（object 不在・rename 相当）。
	w := &Worker{
		store:                    store,
		checkerConfigKey:         "checker_configs.json",
		excludedTitleKeywordsKey: "excluded_title_keywords.json",
	}
	if err := w.refreshVariableConfig(context.Background()); err == nil {
		t.Fatal("refreshVariableConfig should fail when excluded_title_keywords.json is missing")
	}
}

func TestLoadExcludedKeywords(t *testing.T) {
	cases := []struct {
		name    string
		body    string // 空文字は seed しない（object 不在）
		absent  bool   // true は object 不在（削除・rename 相当）を意図
		want    []string
		wantErr bool
	}{
		{name: "文字列配列を読み込む", body: `["完結","外伝"]`, want: []string{"完結", "外伝"}},
		{name: "明示的な空配列は除外語なしとして許容", body: `[]`, want: []string{}},
		{name: "object 不在は error", absent: true, wantErr: true},
		{name: "JSON 不正は error", body: `{not-array`, wantErr: true},
		{name: "JSON の null は空配列でなく error", body: `null`, wantErr: true},
		{name: "文字列配列以外の object 型は error", body: `{"a":"b"}`, wantErr: true},
		{name: "数値配列は型不正で error", body: `[1,2,3]`, wantErr: true},
		{name: "文字列スカラーは型不正で error", body: `"完結"`, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := storage.NewMemStore()
			if !tc.absent {
				store.Seed("excluded_title_keywords.json", tc.body)
			}
			got, err := loadExcludedKeywords(context.Background(), store, "excluded_title_keywords.json")
			if tc.wantErr {
				if err == nil {
					t.Fatalf("loadExcludedKeywords err = nil, want error")
				}
				return
			}
			if err != nil {
				t.Fatalf("loadExcludedKeywords err = %v, want nil", err)
			}
			if !equalStringSlice(got, tc.want) {
				t.Errorf("loadExcludedKeywords = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestLoadExcludedKeywords_MissingObjectIsError(t *testing.T) {
	if _, err := loadExcludedKeywords(context.Background(), storage.NewMemStore(), "excluded_title_keywords.json"); err == nil {
		t.Fatal("missing excluded_title_keywords object must be a config load error, not an empty fallback")
	}
}

func TestLoadExcludedKeywords_TransientSError(t *testing.T) {
	if _, err := loadExcludedKeywords(context.Background(), failingConfigStore{}, "excluded_title_keywords.json"); err == nil {
		t.Fatal("transient S3 Get error must be a config load error")
	}
}

func equalStringSlice(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestRequestID_FromLambdaContext(t *testing.T) {
	ctx := lambdacontext.NewContext(context.Background(), &lambdacontext.LambdaContext{
		AwsRequestID:       "req-abc-123",
		InvokedFunctionArn: "arn:aws:lambda:ap-northeast-1:123:function:check-worker",
	})
	if got := requestID(ctx); got != "req-abc-123" {
		t.Errorf("requestID = %q, want req-abc-123", got)
	}
}
