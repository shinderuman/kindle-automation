package storage

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/shinderuman/kindle-automation/internal/domain/book"
)

// SPECIFICATION.md 9.5 は前提不一致時の再読込 merge を最大3回。
const defaultMergeRetries = 3

// ErrTargetRemoved は対象 ASIN が S3 に存在しない（手動削除）ことを示し、再追加せず正常終点とする（SPECIFICATION.md 7.2 target_removed）。
var ErrTargetRemoved = errors.New("target removed")

// 412 で最新本文を読み直して再試行する（SPECIFICATION.md 9.5）。対象 object は SPECIFICATION.md 9.1 の存在必須 object のため、
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

// UpdateOneBook は対象 ASIN のレコードを更新する。対象 ASIN が無ければ手動削除扱いで applied=false を返す（再追加しない、SPECIFICATION.md 7.2 target_removed）。
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

// UpsertBookRecord は ASIN 単位の冪等 upsert。同一 ASIN があれば Amazon 由来 field を更新し CreatedAt と Extra は保持、なければ追加する（SPECIFICATION.md 9.2/9.4, 7.4）。
// 重複実行でレコードが増えず CreatedAt も変わらない。
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

// UpsertBookRecordChanged は Amazon 由来 field の追加・変更有無を返す冪等 upsert（SPECIFICATION.md 9.2/9.4, 13.4）。
// 新規 ASIN、または Title/URL/ReleaseDate/CurrentPrice/MaxPrice の変化で changed=true となる。
// CreatedAt は既存値を保持し、Extra（未知 field）は既存レコード側を保持するため比較から除外する。
func UpsertBookRecordChanged(ctx context.Context, store ObjectStore, key string, target BookRecord) (bool, error) {
	var changed bool
	err := mutateBooks(ctx, store, key, defaultMergeRetries, func(records []BookRecord) ([]BookRecord, error) {
		idx := findBookIndex(records, target.Book.ASIN)
		if idx == -1 {
			changed = true
			return append(records, target), nil
		}
		updated := target.Book
		updated.CreatedAt = records[idx].Book.CreatedAt
		if !bookAmazonFieldsEqual(records[idx].Book, updated) {
			changed = true
		}
		records[idx].Book = updated
		return records, nil
	})
	return changed, err
}

func bookAmazonFieldsEqual(a, b book.KindleBook) bool {
	return a.ASIN == b.ASIN &&
		a.Title == b.Title &&
		a.URL == b.URL &&
		a.ReleaseDate.Equal(b.ReleaseDate) &&
		priceEqual(a.CurrentPrice, b.CurrentPrice) &&
		priceEqual(a.MaxPrice, b.MaxPrice)
}

func priceEqual(a, b book.Price) bool {
	return a.Valid() == b.Valid() && a.Yen() == b.Yen()
}

func findBookIndex(records []BookRecord, asin string) int {
	for i, r := range records {
		if r.Book.ASIN == asin {
			return i
		}
	}
	return -1
}

// SPECIFICATION.md 9.2 の並び順（発売日降順・同日はタイトル昇順）へ整える。同じ ASIN は最初の出現を優先し、
// そのレコードの Extra（未知 field）を含む全情報を保持する（SPECIFICATION.md 9.2, 9.4）。
// domain book.DedupBooks と同じ規則だが BookRecord 単位で処理し Extra を失わない。
// 全ての書込経路（mutateBooks）で保存前に必ず適用し、保存後の本文へ同一 ASIN の重複が残らないようにする。
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

// 同じ ASIN は最初の出現を優先（SPECIFICATION.md 9.2）。ASIN 空のレコードは重複排除対象外でそのまま残す。
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

// 発売日比較を日付レベルへ正規化し、同日レコードが時刻の差で順序を変えないようにする（domain book.sameDay と同等）。
func sameBookDay(a, b time.Time) bool {
	au := a.UTC()
	bu := b.UTC()
	return au.Year() == bu.Year() && au.Month() == bu.Month() && au.Day() == bu.Day()
}
