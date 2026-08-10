package checkworker

import (
	"context"
	"testing"

	"github.com/aws/aws-lambda-go/lambdacontext"

	"github.com/shinderuman/kindle-automation/internal/storage"
)

// refreshVariableConfig は SaleThreshold・除外キーワード・Gist 設定を毎回最新へ反映する。
// warm execution environment でも S3 の変更を次回 invocation へ反映する（SPECIFICATION.md 16）。
func TestRefreshVariableConfig_ReloadsThresholdsKeywordsAndGist(t *testing.T) {
	store := storage.NewMemStore()
	store.Seed("checker_configs.json", `{"SaleChecker":{"Enabled":true,"GistID":"g1","GistFilename":"sale.md","SaleThreshold":100,"PointPercent":10,"PriceChangeAmount":50},"NewReleaseChecker":{"Enabled":true,"GistID":"gn","GistFilename":"new.md"},"PaperToKindleChecker":{"Enabled":true,"GistID":"gp","GistFilename":"paper.md"}}`)
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
	if w.GistDeps.Settings.Sale.ID != "g1" || w.GistDeps.Settings.NewRelease.ID != "gn" || w.GistDeps.Settings.PaperToKindle.ID != "gp" {
		t.Errorf("Gist settings not mapped: %+v", w.GistDeps.Settings)
	}

	// 同一 Worker（cold client 再利用）で S3 設定だけ変更し、再読込を検証する。
	store.Seed("checker_configs.json", `{"SaleChecker":{"Enabled":true,"GistID":"g2","GistFilename":"sale.md","SaleThreshold":200,"PointPercent":20,"PriceChangeAmount":80},"NewReleaseChecker":{"Enabled":true,"GistID":"gn2","GistFilename":"new.md"},"PaperToKindleChecker":{"Enabled":true,"GistID":"gp2","GistFilename":"paper.md"}}`)
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
	if w.GistDeps.Settings.Sale.ID != "g2" {
		t.Errorf("Gist Sale ID after reload = %v, want g2 (warm reload)", w.GistDeps.Settings.Sale.ID)
	}
}

// 不正 checker 設定（閾値0）は refreshVariableConfig で error となり処理を開始しない。
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

// 除外キーワードの読込失敗（decode error）も refreshVariableConfig の error となる。
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

// checker 設定は有効でも excluded_title_keywords.json が不在だと refreshVariableConfig は error となる。
// 必須読込 object の欠落を空 fallback で吸収しない（SPECIFICATION.md 9.1）。
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

// loadExcludedKeywords は excluded_title_keywords.json を必須の文字列配列として読み込む（SPECIFICATION.md 9.1）。
// object 本文の内容ごとに、読込成功・設定読込 error の判定を固定する。
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

// object 不在（削除・rename 相当）は空 fallback せず設定読込 error となる（SPECIFICATION.md 9.1）。
func TestLoadExcludedKeywords_MissingObjectIsError(t *testing.T) {
	if _, err := loadExcludedKeywords(context.Background(), storage.NewMemStore(), "excluded_title_keywords.json"); err == nil {
		t.Fatal("missing excluded_title_keywords object must be a config load error, not an empty fallback")
	}
}

// S3 一時障害相当の Get error も設定読込 error となる。
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

// Lambda context に request ID があれば requestID はそれを返す（SPECIFICATION.md 18.1 aws_request_id）。
func TestRequestID_FromLambdaContext(t *testing.T) {
	ctx := lambdacontext.NewContext(context.Background(), &lambdacontext.LambdaContext{
		AwsRequestID:       "req-abc-123",
		InvokedFunctionArn: "arn:aws:lambda:ap-northeast-1:123:function:check-worker",
	})
	if got := requestID(ctx); got != "req-abc-123" {
		t.Errorf("requestID = %q, want req-abc-123", got)
	}
}
