package sale

import (
	"reflect"
	"testing"
)

func TestEvaluate(t *testing.T) {
	th := Thresholds{SaleThreshold: 151, PointPercent: 20, PriceChangeAmount: 100}

	tests := []struct {
		name string
		in   Input
		want Conditions
	}{
		{
			name: "価格差だけ成立する",
			in:   Input{CurrentPrice: 600, MaxPrice: 800, Points: 0, Coupon: false},
			want: Conditions{PriceDrop: true},
		},
		{
			name: "ポイント数だけ成立する（還元率は閾値未満）",
			in:   Input{CurrentPrice: 2000, MaxPrice: 2000, Points: 200, Coupon: false},
			want: Conditions{Points: true},
		},
		{
			name: "ポイント還元率だけ成立する（ポイント数は閾値未満）",
			in:   Input{CurrentPrice: 600, MaxPrice: 600, Points: 150, Coupon: false},
			want: Conditions{PointRate: true},
		},
		{
			name: "クーポンだけ成立する",
			in:   Input{CurrentPrice: 800, MaxPrice: 800, Points: 0, Coupon: true},
			want: Conditions{Coupon: true},
		},
		{
			name: "4条件が同時に成立する",
			in:   Input{CurrentPrice: 600, MaxPrice: 800, Points: 200, Coupon: true},
			want: Conditions{PriceDrop: true, Points: true, PointRate: true, Coupon: true},
		},
		{
			name: "現在価格0でもポイント数は成立し還元率は0除算せず成立しない",
			in:   Input{CurrentPrice: 0, MaxPrice: 0, Points: 200, Coupon: false},
			want: Conditions{Points: true},
		},
		{
			name: "いずれの条件も成立しない",
			in:   Input{CurrentPrice: 800, MaxPrice: 800, Points: 10, Coupon: false},
			want: Conditions{},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Evaluate(tc.in, th)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("Evaluate = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestEvaluateUsesSaleThresholdForPriceDropAndPoints(t *testing.T) {
	th := Thresholds{SaleThreshold: 151, PointPercent: 20, PriceChangeAmount: 100}

	// 価格差150(<151)は成立せず、ポイント151(>=151)は成立する。同一閾値の兼用を検証。
	in := Input{CurrentPrice: 649, MaxPrice: 799, Points: 151, Coupon: false}
	got := Evaluate(in, th)
	if got.PriceDrop {
		t.Errorf("PriceDrop should be false for diff 150 < SaleThreshold 151")
	}
	if !got.Points {
		t.Errorf("Points should be true for 151 >= SaleThreshold 151")
	}
}

func TestNotificationLines(t *testing.T) {
	th := Thresholds{SaleThreshold: 151, PointPercent: 20, PriceChangeAmount: 100}

	tests := []struct {
		name       string
		in         Input
		couponText string
		want       []string
	}{
		{
			name:       "成立条件を順に列挙しクーポン文言なしは括弧なし",
			in:         Input{CurrentPrice: 600, MaxPrice: 800, Points: 200, Coupon: true},
			couponText: "",
			want: []string{
				"✅ 最高額との価格差 200円",
				"✅ ポイント 200pt",
				"✅ ポイント還元 33.3%",
				"✅ クーポンあり",
			},
		},
		{
			name:       "クーポン文言を取得した場合は括弧内に追加する",
			in:         Input{CurrentPrice: 800, MaxPrice: 800, Points: 0, Coupon: true},
			couponText: "500円OFF",
			want: []string{
				"✅ クーポンあり (500円OFF)",
			},
		},
		{
			name:       "還元率は小数第1位まで表示する",
			in:         Input{CurrentPrice: 1000, MaxPrice: 1000, Points: 200, Coupon: false},
			couponText: "",
			want: []string{
				"✅ ポイント 200pt",
				"✅ ポイント還元 20.0%",
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			conditions := Evaluate(tc.in, th)
			got := NotificationLines(tc.in, conditions, tc.couponText)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("NotificationLines = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestEvaluatePriceChange(t *testing.T) {
	const amount = 100.0

	tests := []struct {
		name       string
		oldCurrent float64
		current    float64
		wantKind   PriceChangeKind
		wantDiff   int
	}{
		{name: "旧価格0は通知しない", oldCurrent: 0, current: 800, wantKind: NoChange, wantDiff: 0},
		{name: "閾値以上の値上がり", oldCurrent: 600, current: 800, wantKind: PriceUp, wantDiff: 200},
		{name: "閾値以上の値下がり", oldCurrent: 800, current: 600, wantKind: PriceDown, wantDiff: -200},
		{name: "閾値未満の変動は通知しない", oldCurrent: 600, current: 650, wantKind: NoChange, wantDiff: 0},
		{name: "境界値ちょうどは値上がり", oldCurrent: 600, current: 700, wantKind: PriceUp, wantDiff: 100},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotKind, gotDiff := EvaluatePriceChange(tc.oldCurrent, tc.current, amount)
			if gotKind != tc.wantKind || gotDiff != tc.wantDiff {
				t.Fatalf("EvaluatePriceChange = (%v, %d), want (%v, %d)", gotKind, gotDiff, tc.wantKind, tc.wantDiff)
			}
		})
	}
}
