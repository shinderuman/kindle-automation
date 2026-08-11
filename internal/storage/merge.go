package storage

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/shinderuman/kindle-automation/internal/domain/book"
)

// defaultMergeRetries は前提不一致時の再読込 merge 回数（SPECIFICATION.md 9.5 は最大3回）。
const defaultMergeRetries = 3

// ErrTargetRemoved は対象 ASIN が S3 に存在しない（手動削除）ことを示す。
// 再追加せず正常終点とする（SPECIFICATION.md 7.2 target_removed）。
var ErrTargetRemoved = errors.New("target removed")

// mutateBooks は Get → mutate → dedup+sort → Put(If-Match) を行い、412 で最新本文を読み直して再試行する。
// maxRetry は再試行回数。対象 object は SPECIFICATION.md 9.1 の存在必須 object のため、
// ErrObjectNotFound を含む全ての Get error を呼出側へ返し、空配列への fallback や
// If-None-Match: * による暗黙の新規作成は行わない。
func mutateBooks(ctx context.Context, store ObjectStore, key string, maxRetry int, mutate func([]BookRecord) ([]BookRecord, error)) error {
	var lastErr error
	for attempt := 0; attempt <= maxRetry; attempt++ {
		obj, err := store.Get(ctx, key)
		if err != nil {
			return fmt.Errorf("get %s: %w", key, err)
		}
		records, err := DecodeBooks(obj.Body)
		if err != nil {
			return fmt.Errorf("decode %s: %w", key, err)
		}
		next, err := mutate(records)
		if err != nil {
			return err
		}
		next = dedupAndSortBookRecords(next)
		body, err := EncodeBooks(next)
		if err != nil {
			return fmt.Errorf("encode %s: %w", key, err)
		}
		if err := store.Put(ctx, key, body, PutOptions{IfMatch: obj.ETag}); err != nil {
			if errors.Is(err, ErrPreconditionFailed) && attempt < maxRetry {
				lastErr = err
				continue
			}
			return fmt.Errorf("put %s: %w", key, err)
		}
		return nil
	}
	return fmt.Errorf("merge %s failed after %d retries: %w", key, maxRetry, lastErr)
}

// UpdateOneBook は対象 ASIN の Book を更新する。対象の Extra（未知 field）と他レコードは保持する。
// 対象 ASIN が無ければ手動削除扱いで applied=false を返す（再追加しない）。
func UpdateOneBook(ctx context.Context, store ObjectStore, key, asin string, update func(book.KindleBook) book.KindleBook) (bool, error) {
	err := mutateBooks(ctx, store, key, defaultMergeRetries, func(records []BookRecord) ([]BookRecord, error) {
		idx := findBookIndex(records, asin)
		if idx == -1 {
			return nil, ErrTargetRemoved
		}
		records[idx].Book = update(records[idx].Book)
		return records, nil
	})
	if errors.Is(err, ErrTargetRemoved) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// UpsertBookRecord は対象レコードを upsert する。同一 ASIN があれば Amazon 由来 field を更新し、
// CreatedAt と Extra は保持し、なければ追加する（SPECIFICATION.md 9.2/9.4）。
// 重複実行でレコードが増えず CreatedAt も変わらない（SPECIFICATION.md 7.4 の ASIN 単位 upsert 冪等性）。
func UpsertBookRecord(ctx context.Context, store ObjectStore, key string, target BookRecord) error {
	return mutateBooks(ctx, store, key, defaultMergeRetries, func(records []BookRecord) ([]BookRecord, error) {
		idx := findBookIndex(records, target.Book.ASIN)
		if idx == -1 {
			return append(records, target), nil
		}
		updated := target.Book
		updated.CreatedAt = records[idx].Book.CreatedAt
		records[idx].Book = updated
		return records, nil
	})
}

// MergeUpcoming は Upcoming を Unprocessed へ条件付き merge する（SPECIFICATION.md 10）。
// 重複 ASIN は Unprocessed 側を優先する。Unprocessed への保存成功後、Upcoming の ETag が
// 開始時と同じ場合だけ Upcoming を空配列へ戻す。処理中に Upcoming が増えた場合は消去しない。
// 返り値の int は Unprocessed へ新規追加した Upcoming 由来の ASIN 件数。
func MergeUpcoming(ctx context.Context, store ObjectStore, unprocessedKey, upcomingKey string, maxRetry int) (int, error) {
	upcomingObj, err := store.Get(ctx, upcomingKey)
	if err != nil {
		return 0, fmt.Errorf("get upcoming %s: %w", upcomingKey, err)
	}
	upcomingStartETag := upcomingObj.ETag
	upcomingRecs, err := DecodeBooks(upcomingObj.Body)
	if err != nil {
		return 0, fmt.Errorf("decode upcoming %s: %w", upcomingKey, err)
	}

	var lastErr error
	for attempt := 0; attempt <= maxRetry; attempt++ {
		unprocessedObj, err := store.Get(ctx, unprocessedKey)
		if err != nil {
			return 0, fmt.Errorf("get unprocessed %s: %w", unprocessedKey, err)
		}
		unprocessedRecs, err := DecodeBooks(unprocessedObj.Body)
		if err != nil {
			return 0, fmt.Errorf("decode unprocessed %s: %w", unprocessedKey, err)
		}
		added := countAdded(upcomingRecs, unprocessedRecs)
		merged := dedupAndSortBookRecords(mergePreferFirst(unprocessedRecs, upcomingRecs))
		body, err := EncodeBooks(merged)
		if err != nil {
			return 0, fmt.Errorf("encode unprocessed %s: %w", unprocessedKey, err)
		}
		if err := store.Put(ctx, unprocessedKey, body, PutOptions{IfMatch: unprocessedObj.ETag}); err != nil {
			if errors.Is(err, ErrPreconditionFailed) && attempt < maxRetry {
				lastErr = err
				continue
			}
			return 0, fmt.Errorf("put unprocessed %s: %w", unprocessedKey, err)
		}
		return clearUpcomingIfUnchanged(ctx, store, upcomingKey, upcomingStartETag, added)
	}
	return 0, fmt.Errorf("merge upcoming failed after %d retries: %w", maxRetry, lastErr)
}

// clearUpcomingIfUnchanged は Upcoming の ETag が開始時と同じ場合だけ空配列へ戻す。
// ETag が変わっていれば処理中に追加された Upcoming があるため消去せず残す（412/ETag 変更とは区別）。
// upcoming_asins.json は SPECIFICATION.md 9.1 の存在必須 object のため、clear 直前の Get が
// ErrObjectNotFound でも空配列化成功とみなさず、他の Get error と同じく文脈付きで error を返す。
// この時点で Unprocessed への merge は commit 済み（SPECIFICATION.md 7.5 の非 transaction）であり、
// 再実行で merge が ASIN 単位で冪等に補完され Upcoming が復元されれば clear が完了する。
func clearUpcomingIfUnchanged(ctx context.Context, store ObjectStore, key, startETag string, added int) (int, error) {
	if startETag == "" {
		return added, nil
	}
	obj, err := store.Get(ctx, key)
	if err != nil {
		return 0, fmt.Errorf("get upcoming for clear %s: %w", key, err)
	}
	if obj.ETag != startETag {
		return added, nil
	}
	body, err := EncodeBooks(nil)
	if err != nil {
		return 0, fmt.Errorf("encode empty upcoming: %w", err)
	}
	if err := store.Put(ctx, key, body, PutOptions{IfMatch: obj.ETag}); err != nil {
		if errors.Is(err, ErrPreconditionFailed) {
			return added, nil
		}
		return 0, fmt.Errorf("put empty upcoming %s: %w", key, err)
	}
	return added, nil
}

// mergePreferFirst は first を優先し、second のうち first に無い ASIN を追加する。
// 各レコードの Extra（未知 field）もそのまま保持する。
func mergePreferFirst(first, second []BookRecord) []BookRecord {
	out := make([]BookRecord, 0, len(first)+len(second))
	out = append(out, first...)
	seen := make(map[string]bool)
	for _, r := range first {
		if r.Book.ASIN != "" {
			seen[r.Book.ASIN] = true
		}
	}
	for _, r := range second {
		if r.Book.ASIN == "" || seen[r.Book.ASIN] {
			continue
		}
		seen[r.Book.ASIN] = true
		out = append(out, r)
	}
	return out
}

// countAdded は upcoming のうち unprocessed に無い ASIN の件数を返す。
func countAdded(upcoming, unprocessed []BookRecord) int {
	seen := make(map[string]bool)
	for _, r := range unprocessed {
		if r.Book.ASIN != "" {
			seen[r.Book.ASIN] = true
		}
	}
	added := 0
	for _, r := range upcoming {
		if r.Book.ASIN != "" && !seen[r.Book.ASIN] {
			added++
			seen[r.Book.ASIN] = true
		}
	}
	return added
}

func findBookIndex(records []BookRecord, asin string) int {
	for i, r := range records {
		if r.Book.ASIN == asin {
			return i
		}
	}
	return -1
}

// dedupAndSortBookRecords は ASIN 重複排除後に SPECIFICATION.md 9.2 の並び順
// （発売日降順・同日の場合はタイトル昇順）へ整える。同じ ASIN は最初の出現を優先し、
// そのレコードの Extra（未知 field）を含む全情報を保持する（SPECIFICATION.md 9.2, 9.4）。
// domain book.DedupBooks と同じ規則だが BookRecord 単位で処理し Extra を失わない。
// 4つの書籍JSONすべての書込経路（mutateBooks・MergeUpcoming）で保存前に必ず適用し、
// 保存後の本文へ同一 ASIN の重複が残らないようにする。
func dedupAndSortBookRecords(records []BookRecord) []BookRecord {
	deduped := dedupBookRecords(records)
	sorted := append([]BookRecord(nil), deduped...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if !sameBookDay(sorted[i].Book.ReleaseDate, sorted[j].Book.ReleaseDate) {
			return sorted[i].Book.ReleaseDate.After(sorted[j].Book.ReleaseDate)
		}
		return sorted[i].Book.Title < sorted[j].Book.Title
	})
	return sorted
}

// dedupBookRecords は ASIN で重複排除する。同じ ASIN は最初の出現を優先し、
// そのレコードの Extra（未知 field）を含む全情報を保持する（SPECIFICATION.md 9.2）。
// ASIN が空のレコードは重複排除の判定対象にできず、そのまま残す。
// domain book.DedupBooks と同じ規則だが BookRecord 単位で処理し Extra を失わない。
func dedupBookRecords(records []BookRecord) []BookRecord {
	seen := make(map[string]struct{})
	out := make([]BookRecord, 0, len(records))
	for _, r := range records {
		if r.Book.ASIN == "" {
			out = append(out, r)
			continue
		}
		if _, exists := seen[r.Book.ASIN]; exists {
			continue
		}
		seen[r.Book.ASIN] = struct{}{}
		out = append(out, r)
	}
	return out
}

// sameBookDay は2つの時刻が同じ UTC 暦日かを返す（domain book.sameDay と同等）。
// 発売日比較を日付レベルへ正規化し、同日レコードが時刻の差で順序を変えないようにする。
func sameBookDay(a, b time.Time) bool {
	au := a.UTC()
	bu := b.UTC()
	return au.Year() == bu.Year() && au.Month() == bu.Month() && au.Day() == bu.Day()
}
