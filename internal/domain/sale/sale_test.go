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
		{name: "値上がり境界値直前は通知しない", oldCurrent: 600, current: 699, wantKind: NoChange, wantDiff: 0},
		{name: "値下がり境界値ちょうどは値下がり", oldCurrent: 700, current: 600, wantKind: PriceDown, wantDiff: -100},
		{name: "値下がり境界値直前は通知しない", oldCurrent: 700, current: 601, wantKind: NoChange, wantDiff: 0},
		{name: "差額は整数へゼロ方向へ切り捨てる", oldCurrent: 600, current: 700.9, wantKind: PriceUp, wantDiff: 100},
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

func TestConditionsAny(t *testing.T) {
	tests := []struct {
		name string
		c    Conditions
		want bool
	}{
		{name: "全条件不成立はfalse", c: Conditions{}, want: false},
		{name: "価格差単独成立はtrue", c: Conditions{PriceDrop: true}, want: true},
		{name: "ポイント数単独成立はtrue", c: Conditions{Points: true}, want: true},
		{name: "ポイント還元率単独成立はtrue", c: Conditions{PointRate: true}, want: true},
		{name: "クーポン単独成立はtrue", c: Conditions{Coupon: true}, want: true},
		{name: "2条件成立はtrue", c: Conditions{Points: true, Coupon: true}, want: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.c.Any(); got != tc.want {
				t.Fatalf("Any = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestEvaluateThresholdBoundaries(t *testing.T) {
	th := Thresholds{SaleThreshold: 151, PointPercent: 20, PriceChangeAmount: 100}

	t.Run("価格差は直前不成立/一致と直後成立", func(t *testing.T) {
		cases := []struct {
			name    string
			current float64
			max     float64
			want    bool
		}{
			{name: "差150は不成立(直前)", current: 800, max: 950, want: false},
			{name: "差151は成立(一致)", current: 800, max: 951, want: true},
			{name: "差152は成立(直後)", current: 800, max: 952, want: true},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				got := Evaluate(Input{CurrentPrice: tc.current, MaxPrice: tc.max}, th).PriceDrop
				if got != tc.want {
					t.Fatalf("PriceDrop = %v, want %v", got, tc.want)
				}
			})
		}
	})

	t.Run("ポイント数は直前不成立/一致と直後成立", func(t *testing.T) {
		cases := []struct {
			name   string
			points int
			want   bool
		}{
			{name: "150ptは不成立(直前)", points: 150, want: false},
			{name: "151ptは成立(一致)", points: 151, want: true},
			{name: "152ptは成立(直後)", points: 152, want: true},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				in := Input{CurrentPrice: 100000, MaxPrice: 100000, Points: tc.points}
				got := Evaluate(in, th).Points
				if got != tc.want {
					t.Fatalf("Points = %v, want %v", got, tc.want)
				}
			})
		}
	})

	t.Run("ポイント還元率は直前不成立/一致と直後成立し丸めず生値で比較する", func(t *testing.T) {
		cases := []struct {
			name    string
			current float64
			points  int
			want    bool
		}{
			{name: "19.83%は不成立(直前)", current: 600, points: 119, want: false},
			{name: "20.0%は成立(一致)", current: 600, points: 120, want: true},
			{name: "20.17%は成立(直後)", current: 600, points: 121, want: true},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				in := Input{CurrentPrice: tc.current, MaxPrice: tc.current, Points: tc.points}
				got := Evaluate(in, th).PointRate
				if got != tc.want {
					t.Fatalf("PointRate = %v, want %v", got, tc.want)
				}
			})
		}
	})
}

func TestEvaluateFirstFetchNoPriceDrop(t *testing.T) {
	th := Thresholds{SaleThreshold: 151, PointPercent: 20, PriceChangeAmount: 100}
	got := Evaluate(Input{CurrentPrice: 600, MaxPrice: 0, Points: 200, Coupon: true}, th)
	if got.PriceDrop {
		t.Errorf("PriceDrop must be false when MaxPrice is uninitialized (first fetch)")
	}
	if !got.Points || !got.Coupon {
		t.Errorf("Points and Coupon must still be evaluated on first fetch: %+v", got)
	}
}

func TestEvaluatePointRateZeroPrice(t *testing.T) {
	th := Thresholds{SaleThreshold: 151, PointPercent: 20, PriceChangeAmount: 100}
	got := Evaluate(Input{CurrentPrice: 0, MaxPrice: 0, Points: 1, Coupon: false}, th)
	if got.PointRate {
		t.Errorf("PointRate must be false for zero price (no division by zero)")
	}
	if got.Points {
		t.Errorf("Points 1 < SaleThreshold 151 must be false")
	}
}

func TestNotificationLinesPointRateRounding(t *testing.T) {
	th := Thresholds{SaleThreshold: 151, PointPercent: 20, PriceChangeAmount: 100}
	cases := []struct {
		name    string
		current float64
		points  int
		want    string
	}{
		{name: "33.33%は33.3%", current: 600, points: 200, want: "✅ ポイント還元 33.3%"},
		{name: "66.67%は66.7%", current: 300, points: 200, want: "✅ ポイント還元 66.7%"},
		{name: "21.67%は21.7%", current: 600, points: 130, want: "✅ ポイント還元 21.7%"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := Input{CurrentPrice: tc.current, MaxPrice: tc.current, Points: tc.points}
			lines := NotificationLines(in, Evaluate(in, th), "")
			for _, l := range lines {
				if l == tc.want {
					return
				}
			}
			t.Fatalf("NotificationLines = %v, want to contain %q", lines, tc.want)
		})
	}
}

func TestNotificationLinesPriceDropTruncation(t *testing.T) {
	th := Thresholds{SaleThreshold: 151, PointPercent: 20, PriceChangeAmount: 100}
	in := Input{CurrentPrice: 600, MaxPrice: 800.9, Points: 0, Coupon: false}
	lines := NotificationLines(in, Evaluate(in, th), "")
	want := "✅ 最高額との価格差 200円"
	for _, l := range lines {
		if l == want {
			return
		}
	}
	t.Fatalf("NotificationLines = %v, want to contain %q", lines, want)
}
