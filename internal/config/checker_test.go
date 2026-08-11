package config

import (
	"context"
	"errors"
	"testing"

	"github.com/shinderuman/kindle-automation/internal/job"
)

func TestDecodeCheckerConfigs_AcceptsExistingShape(t *testing.T) {
	body := []byte(`{
		"ReportFailure": true,
		"SaleChecker": {"Enabled": true, "GistID": "g1", "GistFilename": "sale.md", "ExecutionIntervalMinutes": 5, "SaleThreshold": 151, "PointPercent": 20, "PriceChangeAmount": 100},
		"NewReleaseChecker": {"Enabled": false, "GistID": "g2", "GistFilename": "new.md", "CycleDays": 1.0},
		"PaperToKindleChecker": {"Enabled": true, "GistID": "g3", "GistFilename": "paper.md"}
	}`)
	cfg, err := DecodeCheckerConfigs(body)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !cfg.SaleChecker.Enabled || cfg.SaleChecker.SaleThreshold != 151 {
		t.Errorf("SaleChecker not decoded: %+v", cfg.SaleChecker)
	}
}

func TestDecodeCheckerConfigs_InvalidJSON(t *testing.T) {
	if _, err := DecodeCheckerConfigs([]byte(`{`)); err == nil {
		t.Fatal("want decode error for invalid JSON")
	}
}

func TestValidate_RejectsEnabledCheckerWithoutGist(t *testing.T) {
	cfg := CheckerConfigs{SaleChecker: SaleCheckerConfig{Enabled: true, SaleThreshold: 151, PointPercent: 20, PriceChangeAmount: 100}}
	if err := cfg.Validate(); !errors.Is(err, ErrInvalidCheckerConfig) {
		t.Fatalf("want ErrInvalidCheckerConfig, got %v", err)
	}
}

func TestValidate_RejectsNonPositiveThreshold(t *testing.T) {
	cfg := CheckerConfigs{SaleChecker: SaleCheckerConfig{Enabled: true, GistID: "g", GistFilename: "f", SaleThreshold: 0, PointPercent: 20, PriceChangeAmount: 100}}
	if err := cfg.Validate(); !errors.Is(err, ErrInvalidCheckerConfig) {
		t.Fatalf("want ErrInvalidCheckerConfig for SaleThreshold=0, got %v", err)
	}
}

func TestValidate_RejectsNonPositivePointPercentAndPriceChange(t *testing.T) {
	cases := []struct {
		name   string
		config SaleCheckerConfig
	}{
		{
			name:   "PointPercent=0",
			config: SaleCheckerConfig{Enabled: true, GistID: "g", GistFilename: "f", SaleThreshold: 100, PointPercent: 0, PriceChangeAmount: 100},
		},
		{
			name:   "PriceChangeAmount=0",
			config: SaleCheckerConfig{Enabled: true, GistID: "g", GistFilename: "f", SaleThreshold: 100, PointPercent: 20, PriceChangeAmount: 0},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := CheckerConfigs{SaleChecker: tc.config}
			if err := cfg.Validate(); !errors.Is(err, ErrInvalidCheckerConfig) {
				t.Fatalf("%s: want ErrInvalidCheckerConfig, got %v", tc.name, err)
			}
		})
	}
}

func TestValidate_RejectsOtherEnabledCheckersWithoutGist(t *testing.T) {
	cases := []struct {
		name string
		cfg  CheckerConfigs
	}{
		{name: "NewRelease without gist", cfg: CheckerConfigs{NewReleaseChecker: NewReleaseCheckerConfig{Enabled: true}}},
		{name: "PaperToKindle without gist", cfg: CheckerConfigs{PaperToKindleChecker: PaperToKindleCheckerConfig{Enabled: true}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.cfg.Validate(); !errors.Is(err, ErrInvalidCheckerConfig) {
				t.Fatalf("%s: want ErrInvalidCheckerConfig, got %v", tc.name, err)
			}
		})
	}
}

func TestValidate_RejectsPartialGist(t *testing.T) {
	cases := []struct {
		name string
		cfg  CheckerConfigs
	}{
		{name: "GistID only", cfg: CheckerConfigs{SaleChecker: SaleCheckerConfig{Enabled: true, GistID: "g", GistFilename: "", SaleThreshold: 100, PointPercent: 20, PriceChangeAmount: 50}}},
		{name: "GistFilename only", cfg: CheckerConfigs{SaleChecker: SaleCheckerConfig{Enabled: true, GistID: "", GistFilename: "f", SaleThreshold: 100, PointPercent: 20, PriceChangeAmount: 50}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.cfg.Validate(); !errors.Is(err, ErrInvalidCheckerConfig) {
				t.Fatalf("%s: want ErrInvalidCheckerConfig, got %v", tc.name, err)
			}
		})
	}
}

func TestValidate_AllDisabledPasses(t *testing.T) {
	cfg := CheckerConfigs{
		SaleChecker:          SaleCheckerConfig{Enabled: false},
		NewReleaseChecker:    NewReleaseCheckerConfig{Enabled: false},
		PaperToKindleChecker: PaperToKindleCheckerConfig{Enabled: false},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("all-disabled config should pass: %v", err)
	}
}

func TestDecodeCheckerConfigs_TypeMismatch(t *testing.T) {
	body := []byte(`{"SaleChecker":{"Enabled":true,"SaleThreshold":"not-a-number"}}`)
	if _, err := DecodeCheckerConfigs(body); err == nil {
		t.Fatal("want decode error for type mismatch")
	}
}

func TestValidate_DisabledSaleSkipsThresholdCheck(t *testing.T) {
	cfg := CheckerConfigs{SaleChecker: SaleCheckerConfig{Enabled: false}}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("disabled sale should not validate thresholds: %v", err)
	}
}

func TestValidate_AcceptsFullyConfigured(t *testing.T) {
	cfg := CheckerConfigs{
		SaleChecker:          SaleCheckerConfig{Enabled: true, GistID: "g1", GistFilename: "sale.md", SaleThreshold: 151, PointPercent: 20, PriceChangeAmount: 100},
		NewReleaseChecker:    NewReleaseCheckerConfig{Enabled: true, GistID: "g2", GistFilename: "new.md"},
		PaperToKindleChecker: PaperToKindleCheckerConfig{Enabled: true, GistID: "g3", GistFilename: "paper.md"},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
}

func TestIsEnabled_MapsCheckType(t *testing.T) {
	cfg := CheckerConfigs{
		SaleChecker:          SaleCheckerConfig{Enabled: true},
		NewReleaseChecker:    NewReleaseCheckerConfig{Enabled: false},
		PaperToKindleChecker: PaperToKindleCheckerConfig{Enabled: true},
	}
	cases := []struct {
		checkType job.CheckType
		want      bool
	}{
		{job.CheckSale, true},
		{job.CheckNewRelease, false},
		{job.CheckPaperToKindle, true},
	}
	for _, c := range cases {
		got, err := cfg.IsEnabled(context.Background(), c.checkType)
		if err != nil {
			t.Fatalf("IsEnabled(%s): %v", c.checkType, err)
		}
		if got != c.want {
			t.Errorf("IsEnabled(%s) = %v, want %v", c.checkType, got, c.want)
		}
	}
	if _, err := cfg.IsEnabled(context.Background(), job.CheckType("unknown")); err == nil {
		t.Error("unknown check_type should error")
	}
}

func TestSaleThresholds_ConvertsIntToFloat(t *testing.T) {
	cfg := CheckerConfigs{SaleChecker: SaleCheckerConfig{SaleThreshold: 151, PointPercent: 20, PriceChangeAmount: 100}}
	th := cfg.SaleThresholds()
	if th.SaleThreshold != 151 || th.PointPercent != 20 || th.PriceChangeAmount != 100 {
		t.Errorf("thresholds = %+v", th)
	}
}

func TestGistMeta_MapsGistType(t *testing.T) {
	cfg := CheckerConfigs{
		SaleChecker:          SaleCheckerConfig{GistID: "g1", GistFilename: "sale.md"},
		NewReleaseChecker:    NewReleaseCheckerConfig{GistID: "g2", GistFilename: "new.md"},
		PaperToKindleChecker: PaperToKindleCheckerConfig{GistID: "g3", GistFilename: "paper.md"},
	}
	for _, c := range []struct {
		gistType     string
		wantID       string
		wantFilename string
	}{
		{"sale", "g1", "sale.md"},
		{"new_release", "g2", "new.md"},
		{"paper_to_kindle", "g3", "paper.md"},
	} {
		id, fn, err := cfg.GistMeta(c.gistType)
		if err != nil {
			t.Fatalf("GistMeta(%s): %v", c.gistType, err)
		}
		if id != c.wantID || fn != c.wantFilename {
			t.Errorf("GistMeta(%s) = (%s,%s), want (%s,%s)", c.gistType, id, fn, c.wantID, c.wantFilename)
		}
	}
	if _, _, err := cfg.GistMeta("bogus"); err == nil {
		t.Error("unknown gist_type should error")
	}
}
