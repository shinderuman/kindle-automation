package storage

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/shinderuman/kindle-automation/internal/domain/book"
)

// flakyStore は指定回数だけ Put 直前に別本文を seed して ETag を変更し、
// 呼び出し元の If-Match を前提不一致（412）へ導くテスト用 ObjectStore。
type flakyStore struct {
	*MemStore
	conflicts   int
	conflictKey string
}

func (f *flakyStore) Put(ctx context.Context, key string, body []byte, opts PutOptions) error {
	if f.conflicts > 0 && key == f.conflictKey {
		// ETag を毎回確実に変えるため、残競合回数を埋め込んだ異なる本文を seed する。
		f.MemStore.Seed(key, fmt.Sprintf("[{\"ASIN\":\"B0CNF%05d\",\"Title\":\"conflict\"}]", f.conflicts))
		f.conflicts--
	}
	return f.MemStore.Put(ctx, key, body, opts)
}

func seedBook(store *MemStore, key string, books ...book.KindleBook) {
	records := make([]BookRecord, len(books))
	for i, b := range books {
		records[i] = BookRecord{Book: b}
	}
	body, err := EncodeBooks(records)
	if err != nil {
		panic(err)
	}
	store.Seed(key, string(body))
}

func TestUpdateOneBook_UpdatesTargetAndKeepsOthers(t *testing.T) {
	store := NewMemStore()
	seedBook(store, "k",
		book.KindleBook{ASIN: "B0TARGET001", Title: "旧", CurrentPrice: book.NewPrice(700), MaxPrice: book.NewPrice(700)},
		book.KindleBook{ASIN: "B0MANUAL0001", Title: "手動"},
	)

	applied, err := UpdateOneBook(context.Background(), store, "k", "B0TARGET001", func(b book.KindleBook) book.KindleBook {
		b.CurrentPrice = book.NewPrice(759)
		b.Title = "新"
		return b
	})
	if err != nil {
		t.Fatalf("UpdateOneBook: %v", err)
	}
	if !applied {
		t.Fatal("applied = false, want true")
	}

	obj, _ := store.Get(context.Background(), "k")
	records, _ := DecodeBooks(obj.Body)
	if len(records) != 2 {
		t.Fatalf("len = %d, want 2 (別ASINを保持)", len(records))
	}
	target := records[findBookIndex(records, "B0TARGET001")]
	if target.Book.Title != "新" || target.Book.CurrentPrice.Yen() != 759 {
		t.Errorf("target not updated: %+v", target.Book)
	}
	if findBookIndex(records, "B0MANUAL0001") == -1 {
		t.Errorf("manual ASIN removed")
	}
}

func TestUpdateOneBook_ManualDeleteIsNotReadded(t *testing.T) {
	store := NewMemStore()
	seedBook(store, "k", book.KindleBook{ASIN: "B0KEEP000001", Title: "keep"})

	applied, err := UpdateOneBook(context.Background(), store, "k", "B0REMOVED001", func(b book.KindleBook) book.KindleBook {
		t.Fatal("update called for removed target")
		return b
	})
	if err != nil {
		t.Fatalf("UpdateOneBook: %v", err)
	}
	if applied {
		t.Errorf("applied = true, want false for removed target")
	}
	obj, _ := store.Get(context.Background(), "k")
	records, _ := DecodeBooks(obj.Body)
	if len(records) != 1 || findBookIndex(records, "B0REMOVED001") != -1 {
		t.Errorf("removed target re-added: %+v", records)
	}
}

func TestUpdateOneBook_KeepsUnknownFields(t *testing.T) {
	store := NewMemStore()
	store.Seed("k", `[{"ASIN":"B0FX3X569X","Title":"T","ReleaseDate":"2026-01-01T00:00:00Z","CurrentPrice":0,"MaxPrice":0,"URL":"","CreatedAt":"2026-01-01T00:00:00Z","Memo":"手動"}]`)

	_, err := UpdateOneBook(context.Background(), store, "k", "B0FX3X569X", func(b book.KindleBook) book.KindleBook {
		b.Title = "更新"
		return b
	})
	if err != nil {
		t.Fatalf("UpdateOneBook: %v", err)
	}
	obj, _ := store.Get(context.Background(), "k")
	records, _ := DecodeBooks(obj.Body)
	if _, ok := records[0].Extra["Memo"]; !ok {
		t.Errorf("unknown field Memo lost: %s", obj.Body)
	}
	if records[0].Book.Title != "更新" {
		t.Errorf("title not updated")
	}
}

func TestUpsertBookRecord_IdempotentNoDuplication(t *testing.T) {
	store := NewMemStore()
	target := BookRecord{Book: book.KindleBook{ASIN: "B0FX3X569X", Title: "T1"}}

	if err := UpsertBookRecord(context.Background(), store, "k", target); err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	if err := UpsertBookRecord(context.Background(), store, "k", target); err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	obj, _ := store.Get(context.Background(), "k")
	records, _ := DecodeBooks(obj.Body)
	if len(records) != 1 {
		t.Fatalf("len = %d, want 1 (重複実行で増加しない)", len(records))
	}
}

func TestUpsertBookRecord_KeepsExistingExtra(t *testing.T) {
	store := NewMemStore()
	store.Seed("k", `[{"ASIN":"B0FX3X569X","Title":"T","ReleaseDate":"2026-01-01T00:00:00Z","CurrentPrice":0,"MaxPrice":0,"URL":"","CreatedAt":"2026-01-01T00:00:00Z","Memo":"手動"}]`)

	target := BookRecord{Book: book.KindleBook{ASIN: "B0FX3X569X", Title: "更新"}}
	if err := UpsertBookRecord(context.Background(), store, "k", target); err != nil {
		t.Fatalf("UpsertBookRecord: %v", err)
	}
	obj, _ := store.Get(context.Background(), "k")
	records, _ := DecodeBooks(obj.Body)
	if _, ok := records[0].Extra["Memo"]; !ok {
		t.Errorf("existing unknown field Memo lost on upsert")
	}
	if records[0].Book.Title != "更新" {
		t.Errorf("title not updated on upsert")
	}
}

func TestMutateBooks_RetriesOnPreconditionFailed(t *testing.T) {
	store := &flakyStore{MemStore: NewMemStore(), conflicts: 2, conflictKey: "k"}
	store.Seed("k", `[]`)

	calls := 0
	err := mutateBooks(context.Background(), store, "k", 3, func(records []BookRecord) ([]BookRecord, error) {
		calls++
		return append(records, BookRecord{Book: book.KindleBook{ASIN: "B0FX3X569X"}}), nil
	})
	if err != nil {
		t.Fatalf("mutateBooks: %v", err)
	}
	if calls != 3 {
		t.Errorf("mutate calls = %d, want 3 (2 conflicts + 1 success)", calls)
	}
}

func TestMutateBooks_FailsAfterMaxRetries(t *testing.T) {
	store := &flakyStore{MemStore: NewMemStore(), conflicts: 10, conflictKey: "k"}
	store.Seed("k", `[]`)

	err := mutateBooks(context.Background(), store, "k", 3, func(records []BookRecord) ([]BookRecord, error) {
		return append(records, BookRecord{Book: book.KindleBook{ASIN: "B0FX3X569X"}}), nil
	})
	if err == nil {
		t.Fatal("mutateBooks should fail after max retries")
	}
}

func TestMergeUpcoming_PrefersUnprocessedAndClearsWhenUnchanged(t *testing.T) {
	store := NewMemStore()
	seedBook(store, "unprocessed", book.KindleBook{
		ASIN: "A", Title: "unprocessed-A", ReleaseDate: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		CurrentPrice: book.NewPrice(100), MaxPrice: book.NewPrice(100),
		URL: "ua", CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	})
	seedBook(store, "upcoming",
		book.KindleBook{ASIN: "A", Title: "upcoming-A", ReleaseDate: time.Date(2026, 2, 2, 0, 0, 0, 0, time.UTC),
			CurrentPrice: book.NewPrice(200), MaxPrice: book.NewPrice(200), URL: "ua2", CreatedAt: time.Date(2026, 2, 2, 0, 0, 0, 0, time.UTC)},
		book.KindleBook{ASIN: "B", Title: "upcoming-B", ReleaseDate: time.Date(2026, 3, 3, 0, 0, 0, 0, time.UTC),
			CurrentPrice: book.NewPrice(300), MaxPrice: book.NewPrice(300), URL: "ub", CreatedAt: time.Date(2026, 3, 3, 0, 0, 0, 0, time.UTC)},
	)

	added, err := MergeUpcoming(context.Background(), store, "unprocessed", "upcoming", 3)
	if err != nil {
		t.Fatalf("MergeUpcoming: %v", err)
	}
	if added != 1 {
		t.Errorf("added = %d, want 1 (B のみ)", added)
	}

	obj, _ := store.Get(context.Background(), "unprocessed")
	records, _ := DecodeBooks(obj.Body)
	a := records[findBookIndex(records, "A")]
	if a.Book.Title != "unprocessed-A" {
		t.Errorf("unprocessed 側を優先していない: %s", a.Book.Title)
	}
	if findBookIndex(records, "B") == -1 {
		t.Errorf("B が unprocessed へ追加されていない")
	}

	upcomingObj, _ := store.Get(context.Background(), "upcoming")
	if strings.TrimSpace(string(upcomingObj.Body)) != "[]" {
		t.Errorf("upcoming が空配列化されていない: %s", upcomingObj.Body)
	}
}

func TestClearUpcomingIfUnchanged_KeepsWhenChangedDuringMerge(t *testing.T) {
	store := NewMemStore()
	seedBook(store, "upcoming", book.KindleBook{ASIN: "C", Title: "original"})
	obj, _ := store.Get(context.Background(), "upcoming")
	startETag := obj.ETag

	// 処理中に Upcoming が追加された想定で本文を変更する。
	seedBook(store, "upcoming",
		book.KindleBook{ASIN: "C", Title: "original"},
		book.KindleBook{ASIN: "D", Title: "added-during-merge"},
	)

	added, err := clearUpcomingIfUnchanged(context.Background(), store, "upcoming", startETag, 1)
	if err != nil {
		t.Fatalf("clearUpcomingIfUnchanged: %v", err)
	}
	if added != 1 {
		t.Errorf("added = %d, want 1", added)
	}
	upcomingObj, _ := store.Get(context.Background(), "upcoming")
	records, _ := DecodeBooks(upcomingObj.Body)
	if len(records) != 2 {
		t.Errorf("処理中に増えた Upcoming を消去した: len=%d", len(records))
	}
}

// asinOrder はレコードの ASIN を並び順どおりに取り出す（保存順検証用）。
func asinOrder(records []BookRecord) []string {
	out := make([]string, len(records))
	for i, r := range records {
		out[i] = r.Book.ASIN
	}
	return out
}

// asinsEqual は ASIN の並びが完全一致するかを返す。
func asinsEqual(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func TestUpsertBookRecord_SavesSortOrder(t *testing.T) {
	store := NewMemStore()
	// 発売日昇順かつ同日ペアを逆順で seed。未知 field も混ぜる。
	store.Seed("k", `[
        {"ASIN":"B0OLDEST001","Title":"Z","ReleaseDate":"2025-01-01T00:00:00Z","CurrentPrice":0,"MaxPrice":0,"URL":"","CreatedAt":"2025-01-01T00:00:00Z"},
        {"ASIN":"B0SAME000001","Title":"BBB","ReleaseDate":"2026-03-03T00:00:00Z","CurrentPrice":0,"MaxPrice":0,"URL":"","CreatedAt":"2026-03-03T00:00:00Z","Memo":"手動"},
        {"ASIN":"B0SAME000002","Title":"AAA","ReleaseDate":"2026-03-03T00:00:00Z","CurrentPrice":0,"MaxPrice":0,"URL":"","CreatedAt":"2026-03-03T00:00:00Z"}
    ]`)
	// 最新発売日の新規レコードを upsert し、保存順が発売日降順・同日タイトル昇順になるか。
	if err := UpsertBookRecord(context.Background(), store, "k", BookRecord{Book: book.KindleBook{
		ASIN: "B0NEWEST001", Title: "A", ReleaseDate: time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC), CreatedAt: time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC),
	}}); err != nil {
		t.Fatalf("UpsertBookRecord: %v", err)
	}
	obj, _ := store.Get(context.Background(), "k")
	records, _ := DecodeBooks(obj.Body)
	want := []string{"B0NEWEST001", "B0SAME000002", "B0SAME000001", "B0OLDEST001"}
	if !asinsEqual(asinOrder(records), want) {
		t.Errorf("order = %v, want %v\n%s", asinOrder(records), want, obj.Body)
	}
	// 同日タイトル昇順側の未知 field が保存順適用後も保持されるか（SPECIFICATION.md 9.4）。
	if _, ok := records[findBookIndex(records, "B0SAME000001")].Extra["Memo"]; !ok {
		t.Errorf("unknown field Memo lost after sort: %s", obj.Body)
	}
}

func TestMergeUpcoming_SavesSortOrder(t *testing.T) {
	store := NewMemStore()
	// unprocessed 側は古く、upcoming 側に新しい日を混ぜる。統合後に発売日降順になるか。
	seedBook(store, "unprocessed", book.KindleBook{
		ASIN: "B0OLD", Title: "old", ReleaseDate: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
	})
	seedBook(store, "upcoming",
		book.KindleBook{ASIN: "B0MID", Title: "mid", ReleaseDate: time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)},
		book.KindleBook{ASIN: "B0NEW", Title: "new", ReleaseDate: time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC)},
	)
	if _, err := MergeUpcoming(context.Background(), store, "unprocessed", "upcoming", 3); err != nil {
		t.Fatalf("MergeUpcoming: %v", err)
	}
	obj, _ := store.Get(context.Background(), "unprocessed")
	records, _ := DecodeBooks(obj.Body)
	want := []string{"B0NEW", "B0MID", "B0OLD"}
	if !asinsEqual(asinOrder(records), want) {
		t.Errorf("order = %v, want %v\n%s", asinOrder(records), want, obj.Body)
	}
}
