// Package sale はセール条件と価格変動の業務判定を提供する。
// すべて外部サービスへ依存しない純粋関数で、閾値は引数で受け取る。
package sale

import "fmt"

// Thresholds はセールと価格変動の閾値（SPECIFICATION.md 16）。
// SaleThreshold は価格差とポイント数の双方に使用する（既存Go実装と同じ）。
type Thresholds struct {
	SaleThreshold     float64
	PointPercent      float64
	PriceChangeAmount float64
}

// Input.MaxPrice は未取得の場合、呼び出し側で 0 を渡す。
// 紙書籍価格はセール条件に使用しない（SPECIFICATION.md 12.4）。
type Input struct {
	CurrentPrice float64
	MaxPrice     float64
	Points       int
	Coupon       bool
}

// Conditions は4つの独立したセール条件（SPECIFICATION.md 12.4）。主従を設けない。
type Conditions struct {
	PriceDrop bool
	Points    bool
	PointRate bool
	Coupon    bool
}

func (c Conditions) Any() bool {
	return c.PriceDrop || c.Points || c.PointRate || c.Coupon
}

func Evaluate(in Input, th Thresholds) Conditions {
	c := Conditions{}
	if in.MaxPrice-in.CurrentPrice >= th.SaleThreshold {
		c.PriceDrop = true
	}
	if float64(in.Points) >= th.SaleThreshold {
		c.Points = true
	}
	if in.CurrentPrice > 0 && pointRatePercent(in) >= th.PointPercent {
		c.PointRate = true
	}
	if in.Coupon {
		c.Coupon = true
	}
	return c
}

// pointRatePercent は呼び出し側で CurrentPrice > 0 を保証すること（0 除算回避）。
func pointRatePercent(in Input) float64 {
	return float64(in.Points) / in.CurrentPrice * 100
}

// NotificationLines は成立した条件を通知条件名として列挙する（SPECIFICATION.md 12.4/17.1）。
// couponText が空でなければクーポン条件へ括弧内に文言を追加する。
func NotificationLines(in Input, c Conditions, couponText string) []string {
	var lines []string
	if c.PriceDrop {
		lines = append(lines, fmt.Sprintf("✅ 最高額との価格差 %d円", int(in.MaxPrice-in.CurrentPrice)))
	}
	if c.Points {
		lines = append(lines, fmt.Sprintf("✅ ポイント %dpt", in.Points))
	}
	if c.PointRate {
		lines = append(lines, fmt.Sprintf("✅ ポイント還元 %.1f%%", pointRatePercent(in)))
	}
	if c.Coupon {
		if couponText != "" {
			lines = append(lines, fmt.Sprintf("✅ クーポンあり (%s)", couponText))
		} else {
			lines = append(lines, "✅ クーポンあり")
		}
	}
	return lines
}

type PriceChangeKind int

const (
	NoChange PriceChangeKind = iota
	PriceUp
	PriceDown
)

// EvaluatePriceChange は oldCurrent が 0 の場合は通知しない（SPECIFICATION.md 12.5）。
// セール条件成立時に価格変動を送らない排他は呼び出し側で行う。
func EvaluatePriceChange(oldCurrent, current float64, amount float64) (PriceChangeKind, int) {
	if oldCurrent == 0 {
		return NoChange, 0
	}
	diff := current - oldCurrent
	switch {
	case diff >= amount:
		return PriceUp, int(diff)
	case diff <= -amount:
		return PriceDown, int(diff)
	default:
		return NoChange, 0
	}
}
