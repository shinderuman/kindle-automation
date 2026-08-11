package book

import (
	"errors"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// ErrInvalidReleaseDate は発売日文字列を解析できなかったことを示す。
var ErrInvalidReleaseDate = errors.New("invalid release date")

var (
	japaneseDateRe = regexp.MustCompile(`(\d{4})年(\d{1,2})月(\d{1,2})日`)
	slashDateRe    = regexp.MustCompile(`(\d{4})/(\d{1,2})/(\d{1,2})`)
)

// ParseReleaseDate は発売日文字列を UTC 00:00:00 へ正規化する（SPECIFICATION.md 11.2）。
// UserScript の new Date() コンストラクタ任せやローカル時刻生成は採用せず、
// 抽出した年月日を直接 UTC 00:00:00 の time.Time へ組む。
func ParseReleaseDate(text string) (time.Time, error) {
	trimmed := strings.TrimSpace(text)
	if m := japaneseDateRe.FindStringSubmatch(trimmed); m != nil {
		return buildUTCDate(m[1], m[2], m[3])
	}
	if m := slashDateRe.FindStringSubmatch(trimmed); m != nil {
		return buildUTCDate(m[1], m[2], m[3])
	}
	return time.Time{}, ErrInvalidReleaseDate
}

func buildUTCDate(yearStr, monthStr, dayStr string) (time.Time, error) {
	year, err := strconv.Atoi(yearStr)
	if err != nil {
		return time.Time{}, ErrInvalidReleaseDate
	}
	month, err := strconv.Atoi(monthStr)
	if err != nil {
		return time.Time{}, ErrInvalidReleaseDate
	}
	day, err := strconv.Atoi(dayStr)
	if err != nil {
		return time.Time{}, ErrInvalidReleaseDate
	}
	return time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC), nil
}

// sameDay は比較の安全のため時刻を UTC 暦日レベルで比較する（発売日は UTC 00:00:00 へ正規化済み）。
func sameDay(a, b time.Time) bool {
	au := a.UTC()
	bu := b.UTC()
	return au.Year() == bu.Year() && au.Month() == bu.Month() && au.Day() == bu.Day()
}
