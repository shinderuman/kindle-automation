package config

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	domainsale "github.com/shinderuman/kindle-automation/internal/domain/sale"
	"github.com/shinderuman/kindle-automation/internal/job"
)

// ErrInvalidCheckerConfig は checker_configs.json の validation 失敗。
// 閾値が正でない、Enabled Checker に Gist 設定がない等（SPECIFICATION.md 16）。
var ErrInvalidCheckerConfig = errors.New("invalid checker config")

// CheckerConfigs は checker_configs.json の新システム使用部分（SPECIFICATION.md 16）。
// PA API retry・CycleDays・ExecutionIntervalMinutes・ReportFailure 等、新旧で使わない field は
// JSON に残っていても decode 対象外とし、本構造体へは持たない。
type CheckerConfigs struct {
	SaleChecker          SaleCheckerConfig          `json:"SaleChecker"`
	NewReleaseChecker    NewReleaseCheckerConfig    `json:"NewReleaseChecker"`
	PaperToKindleChecker PaperToKindleCheckerConfig `json:"PaperToKindleChecker"`
}

// SaleCheckerConfig.SaleThreshold は価格差とポイント数の双方に使う。
type SaleCheckerConfig struct {
	Enabled           bool   `json:"Enabled"`
	GistID            string `json:"GistID"`
	GistFilename      string `json:"GistFilename"`
	SaleThreshold     int    `json:"SaleThreshold"`
	PointPercent      int    `json:"PointPercent"`
	PriceChangeAmount int    `json:"PriceChangeAmount"`
}

type NewReleaseCheckerConfig struct {
	Enabled      bool   `json:"Enabled"`
	GistID       string `json:"GistID"`
	GistFilename string `json:"GistFilename"`
}

type PaperToKindleCheckerConfig struct {
	Enabled      bool   `json:"Enabled"`
	GistID       string `json:"GistID"`
	GistFilename string `json:"GistFilename"`
}

func DecodeCheckerConfigs(data []byte) (CheckerConfigs, error) {
	var result CheckerConfigs
	if err := json.Unmarshal(data, &result); err != nil {
		return CheckerConfigs{}, fmt.Errorf("decode checker config: %w", err)
	}
	return result, nil
}

// Validate は SPECIFICATION.md 16 の起動時 validation を行う。
// 使用する閾値が正の値であること、Enabled な Checker に Gist 設定があることを検証する。
// 不正値をデフォルトで補完せず、error を返す。
func (c CheckerConfigs) Validate() error {
	if c.SaleChecker.Enabled {
		if err := requireGist("SaleChecker", c.SaleChecker.GistID, c.SaleChecker.GistFilename); err != nil {
			return err
		}
		if c.SaleChecker.SaleThreshold <= 0 || c.SaleChecker.PointPercent <= 0 || c.SaleChecker.PriceChangeAmount <= 0 {
			return fmt.Errorf("%w: SaleChecker thresholds must be positive", ErrInvalidCheckerConfig)
		}
	}
	if c.NewReleaseChecker.Enabled {
		if err := requireGist("NewReleaseChecker", c.NewReleaseChecker.GistID, c.NewReleaseChecker.GistFilename); err != nil {
			return err
		}
	}
	if c.PaperToKindleChecker.Enabled {
		if err := requireGist("PaperToKindleChecker", c.PaperToKindleChecker.GistID, c.PaperToKindleChecker.GistFilename); err != nil {
			return err
		}
	}
	return nil
}

func requireGist(name, id, filename string) error {
	if id == "" || filename == "" {
		return fmt.Errorf("%w: %s needs GistID and GistFilename", ErrInvalidCheckerConfig, name)
	}
	return nil
}

// IsEnabled は dispatch.ConfigReader として各 Checker の Enabled を返す。
// 未知の check_type は error とする。
func (c CheckerConfigs) IsEnabled(_ context.Context, checkType job.CheckType) (bool, error) {
	switch checkType {
	case job.CheckSale:
		return c.SaleChecker.Enabled, nil
	case job.CheckNewRelease:
		return c.NewReleaseChecker.Enabled, nil
	case job.CheckPaperToKindle:
		return c.PaperToKindleChecker.Enabled, nil
	default:
		return false, fmt.Errorf("unknown check_type %q", checkType)
	}
}

func (c CheckerConfigs) SaleThresholds() domainsale.Thresholds {
	return domainsale.Thresholds{
		SaleThreshold:     float64(c.SaleChecker.SaleThreshold),
		PointPercent:      float64(c.SaleChecker.PointPercent),
		PriceChangeAmount: float64(c.SaleChecker.PriceChangeAmount),
	}
}

// GistMeta は gist_type に対応する GistID と filename を返す。
// gist_type は sale / new_release / paper_to_kindle（SPECIFICATION.md 15）。
func (c CheckerConfigs) GistMeta(gistType string) (string, string, error) {
	switch gistType {
	case "sale":
		return c.SaleChecker.GistID, c.SaleChecker.GistFilename, nil
	case "new_release":
		return c.NewReleaseChecker.GistID, c.NewReleaseChecker.GistFilename, nil
	case "paper_to_kindle":
		return c.PaperToKindleChecker.GistID, c.PaperToKindleChecker.GistFilename, nil
	default:
		return "", "", fmt.Errorf("unknown gist_type %q", gistType)
	}
}
