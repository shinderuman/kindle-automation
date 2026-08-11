package storage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
	store.Seed("k", `[]`)
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

// failStore は Get/Put で固定の非前提不一致 error を返す検証用 store。
// Get error と Put 非412 error が retry されず即時伝播することを検証する（SPECIFICATION.md 9.5 error 分類）。
type failStore struct {
	getErr   error
	putErr   error
	getCalls int
	putCalls int
	lastOpts PutOptions
}

func (s *failStore) Get(_ context.Context, _ string) (Object, error) {
	s.getCalls++
	if s.getErr != nil {
		return Object{}, s.getErr
	}
	return Object{Body: []byte("[]"), ETag: "etag-fail"}, nil
}

func (s *failStore) Put(_ context.Context, _ string, _ []byte, opts PutOptions) error {
	s.putCalls++
	s.lastOpts = opts
	if s.putErr != nil {
		return s.putErr
	}
	return nil
}

// ctxErrStore は context の取消/超過を store 層が表面化した場合の検証用 store。
// MemStore は ctx を無視するため、ctx.Err() を伝える store で cancel 伝播を検証する。
type ctxErrStore struct{}

func (s *ctxErrStore) Get(ctx context.Context, _ string) (Object, error) {
	if err := ctx.Err(); err != nil {
		return Object{}, err
	}
	return Object{Body: []byte("[]"), ETag: ""}, nil
}

func (s *ctxErrStore) Put(ctx context.Context, _ string, _ []byte, _ PutOptions) error {
	return ctx.Err()
}

// keyedGetErrStore は指定 key の Get だけ固定 error を返し、他は MemStore へ委譲する。
// MergeUpcoming で特定 object の Get 失敗時の挙動（upcoming を消去しない等）を検証する。
type keyedGetErrStore struct {
	*MemStore
	failKey string
	getErr  error
}

func (s *keyedGetErrStore) Get(ctx context.Context, key string) (Object, error) {
	if key == s.failKey && s.getErr != nil {
		return Object{}, s.getErr
	}
	return s.MemStore.Get(ctx, key)
}

// manualConflictStore は unprocessed の初回 Put 直前に手動追加レコードを紛れ込ませ
// ETag を変えて 412 を起こす。retry で再読込した本文に手動追加が残ることを検証する。
type manualConflictStore struct {
	*MemStore
	conflicted bool
}

func (s *manualConflictStore) Put(ctx context.Context, key string, body []byte, opts PutOptions) error {
	if key == "unprocessed" && !s.conflicted {
		// 既存 unprocessed へ手動で1件追加された（ETag 変更）状態を再現する。
		s.Seed("unprocessed", `[{"ASIN":"B0MANUAL001","Title":"手動","ReleaseDate":"2026-01-01T00:00:00Z","CurrentPrice":0,"MaxPrice":0,"URL":"","CreatedAt":"2026-01-01T00:00:00Z"}]`)
		s.conflicted = true
		// 呼び出し元の If-Match は旧 ETag のため前提不一致になる。
		return ErrPreconditionFailed
	}
	return s.MemStore.Put(ctx, key, body, opts)
}

// errMutateFailure は即時伝播テストで「retry で再呼び出しされない」ことを確認するための固有 error。
var errMutateFailure = errors.New("mutate failure sentinel")

// TestMutateBooks_GetErrorNotRetried は Get の非 NotFound error を retry せず即時返すことを検証する。
func TestMutateBooks_GetErrorNotRetried(t *testing.T) {
	store := &failStore{getErr: errMutateFailure}
	err := mutateBooks(context.Background(), store, "k", 3, func(records []BookRecord) ([]BookRecord, error) {
		t.Fatal("mutate must not run when Get fails")
		return records, nil
	})
	if !errors.Is(err, errMutateFailure) {
		t.Errorf("err = %v, want sentinel", err)
	}
	if store.getCalls != 1 {
		t.Errorf("getCalls = %d, want 1 (Get error must not retry)", store.getCalls)
	}
}

// TestMutateBooks_PutErrorNotPreconditionNotRetried は Put の非412 error を retry せず即時返すことを検証する。
func TestMutateBooks_PutErrorNotPreconditionNotRetried(t *testing.T) {
	store := &failStore{putErr: errMutateFailure}
	err := mutateBooks(context.Background(), store, "k", 3, func(records []BookRecord) ([]BookRecord, error) {
		return append(records, BookRecord{Book: book.KindleBook{ASIN: "B0FX3X569X"}}), nil
	})
	if !errors.Is(err, errMutateFailure) {
		t.Errorf("err = %v, want sentinel", err)
	}
	if store.putCalls != 1 {
		t.Errorf("putCalls = %d, want 1 (non-412 Put error must not retry)", store.putCalls)
	}
}

// TestMutateBooks_ContextCancelPropagates は ctx 取消時に store が ctx.Err() を返せば
// mutateBooks がそれを retry/握り潰しせず伝播することを検証する。
func TestMutateBooks_ContextCancelPropagates(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := mutateBooks(ctx, &ctxErrStore{}, "k", 3, func(records []BookRecord) ([]BookRecord, error) {
		t.Fatal("mutate must not run when ctx cancelled before Get")
		return records, nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}

// optsCaptureStore は MemStore へ委譲しつつ Put に渡された opts を記録する。
// 既存 object 更新で必ず If-Match が設定される（無条件上書き経路がない）ことを検証する。
type optsCaptureStore struct {
	*MemStore
	lastOpts PutOptions
	putCalls int
}

func (s *optsCaptureStore) Put(ctx context.Context, key string, body []byte, opts PutOptions) error {
	s.lastOpts = opts
	s.putCalls++
	return s.MemStore.Put(ctx, key, body, opts)
}

// TestMutateBooks_AlwaysUsesIfMatchOnExistingObject は既存 object の更新で
// If-Match が空でない（無条件上書きでない）ことを回帰検証する（SPECIFICATION.md 9.5）。
func TestMutateBooks_AlwaysUsesIfMatchOnExistingObject(t *testing.T) {
	store := &optsCaptureStore{MemStore: NewMemStore()}
	seedBook(store.MemStore, "k", book.KindleBook{ASIN: "B0TARGET001", Title: "旧"})
	obj, _ := store.Get(context.Background(), "k")

	_, err := UpdateOneBook(context.Background(), store, "k", "B0TARGET001", func(b book.KindleBook) book.KindleBook {
		b.Title = "新"
		return b
	})
	if err != nil {
		t.Fatalf("UpdateOneBook: %v", err)
	}
	if store.putCalls != 1 {
		t.Errorf("putCalls = %d, want 1", store.putCalls)
	}
	if store.lastOpts.IfMatch == "" {
		t.Errorf("IfMatch empty on existing object (unconditional overwrite path): %+v", store.lastOpts)
	}
	if store.lastOpts.IfMatch != obj.ETag {
		t.Errorf("IfMatch = %q, want object ETag %q", store.lastOpts.IfMatch, obj.ETag)
	}
	if store.lastOpts.IfNoneMatch != "" {
		t.Errorf("IfNoneMatch must be empty on existing object: %+v", store.lastOpts)
	}
}

// TestMutateBooks_MissingObjectErrors は必須 object が存在しない場合に空配列へ fallback せず
// If-None-Match: * で新規作成もせず、Get error を返すことを検証する（SPECIFICATION.md 9.1/9.5）。
// 汎用 store が object 欠落を暗黙に空配列化・新規作成しない回帰保護。
func TestMutateBooks_MissingObjectErrors(t *testing.T) {
	store := &optsCaptureStore{MemStore: NewMemStore()}
	err := UpsertBookRecord(context.Background(), store, "k", BookRecord{Book: book.KindleBook{ASIN: "B0FX3X569X"}})
	if !errors.Is(err, ErrObjectNotFound) {
		t.Fatalf("err = %v, want wrap of ErrObjectNotFound", err)
	}
	if store.putCalls != 0 {
		t.Errorf("putCalls = %d, want 0 (object 欠落時は書込んではいけない)", store.putCalls)
	}
	if _, err := store.Get(context.Background(), "k"); !errors.Is(err, ErrObjectNotFound) {
		t.Errorf("object must not be created on missing: %v", err)
	}
}

func TestMergeUpcoming_EmptyUpcomingAddsNothing(t *testing.T) {
	store := NewMemStore()
	seedBook(store, "unprocessed", book.KindleBook{ASIN: "A", Title: "a", ReleaseDate: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)})
	// upcoming は空配列。
	store.Seed("upcoming", `[]`)

	added, err := MergeUpcoming(context.Background(), store, "unprocessed", "upcoming", 3)
	if err != nil {
		t.Fatalf("MergeUpcoming: %v", err)
	}
	if added != 0 {
		t.Errorf("added = %d, want 0 for empty upcoming", added)
	}
	obj, _ := store.Get(context.Background(), "unprocessed")
	records, _ := DecodeBooks(obj.Body)
	if len(records) != 1 || records[0].Book.ASIN != "A" {
		t.Errorf("unprocessed changed unexpectedly: %+v", records)
	}
}

func TestMergeUpcoming_EmptyUnprocessedTakesAllUpcoming(t *testing.T) {
	store := NewMemStore()
	store.Seed("unprocessed", `[]`)
	seedBook(store, "upcoming",
		book.KindleBook{ASIN: "B", Title: "b", ReleaseDate: time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)},
		book.KindleBook{ASIN: "C", Title: "c", ReleaseDate: time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)},
	)

	added, err := MergeUpcoming(context.Background(), store, "unprocessed", "upcoming", 3)
	if err != nil {
		t.Fatalf("MergeUpcoming: %v", err)
	}
	if added != 2 {
		t.Errorf("added = %d, want 2", added)
	}
	obj, _ := store.Get(context.Background(), "unprocessed")
	records, _ := DecodeBooks(obj.Body)
	if findBookIndex(records, "B") == -1 || findBookIndex(records, "C") == -1 {
		t.Errorf("upcoming B/C not merged into empty unprocessed: %+v", records)
	}
}

func TestMergeUpcoming_BothEmptyIsNoOp(t *testing.T) {
	store := NewMemStore()
	store.Seed("unprocessed", `[]`)
	store.Seed("upcoming", `[]`)

	added, err := MergeUpcoming(context.Background(), store, "unprocessed", "upcoming", 3)
	if err != nil {
		t.Fatalf("MergeUpcoming: %v", err)
	}
	if added != 0 {
		t.Errorf("added = %d, want 0", added)
	}
}

// TestMergeUpcoming_RetriesOnUnprocessedConflict は unprocessed の Put が 412 になっても
// 最新本文を再読込して merge を最大3回やり直すことを検証する（SPECIFICATION.md 9.5/10）。
func TestMergeUpcoming_RetriesOnUnprocessedConflict(t *testing.T) {
	store := &flakyStore{MemStore: NewMemStore(), conflicts: 2, conflictKey: "unprocessed"}
	seedBook(store.MemStore, "unprocessed", book.KindleBook{
		ASIN: "A", Title: "a", ReleaseDate: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	})
	seedBook(store.MemStore, "upcoming", book.KindleBook{
		ASIN: "B", Title: "b", ReleaseDate: time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC),
	})

	added, err := MergeUpcoming(context.Background(), store, "unprocessed", "upcoming", 3)
	if err != nil {
		t.Fatalf("MergeUpcoming: %v", err)
	}
	if added != 1 {
		t.Errorf("added = %d, want 1 (B)", added)
	}
	obj, _ := store.Get(context.Background(), "unprocessed")
	records, _ := DecodeBooks(obj.Body)
	if findBookIndex(records, "B") == -1 {
		t.Errorf("upcoming B not merged after retry: %+v", records)
	}
}

// TestMergeUpcoming_KeepsManualUnprocessedDuringRetry は retry 中に unprocessed へ
// 手動追加されたレコードが、再 merge で失われずに残ることを検証する（SPECIFICATION.md 9.4/10）。
func TestMergeUpcoming_KeepsManualUnprocessedDuringRetry(t *testing.T) {
	store := &manualConflictStore{MemStore: NewMemStore()}
	seedBook(store.MemStore, "unprocessed", book.KindleBook{
		ASIN: "A", Title: "a", ReleaseDate: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	})
	seedBook(store.MemStore, "upcoming", book.KindleBook{
		ASIN: "B", Title: "b", ReleaseDate: time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC),
	})

	added, err := MergeUpcoming(context.Background(), store, "unprocessed", "upcoming", 3)
	if err != nil {
		t.Fatalf("MergeUpcoming: %v", err)
	}
	if added != 1 {
		t.Errorf("added = %d, want 1 (B)", added)
	}
	obj, _ := store.Get(context.Background(), "unprocessed")
	records, _ := DecodeBooks(obj.Body)
	// 処理中に紛れ込んだ手動追加 B0MANUAL001 と、upcoming 由来 B が両方残る。
	if findBookIndex(records, "B0MANUAL001") == -1 {
		t.Errorf("手動追加レコードが retry で失われた: %+v", records)
	}
	if findBookIndex(records, "B") == -1 {
		t.Errorf("upcoming B not merged: %+v", records)
	}
}

// TestMergeUpcoming_UnprocessedGetErrorReturnsAndKeepsUpcoming は unprocessed の Get が
// 非 NotFound error のとき、upcoming を消去せず error を返すことを検証する（部分失敗の安全性）。
func TestMergeUpcoming_UnprocessedGetErrorReturnsAndKeepsUpcoming(t *testing.T) {
	store := &keyedGetErrStore{MemStore: NewMemStore(), failKey: "unprocessed", getErr: errMutateFailure}
	seedBook(store.MemStore, "unprocessed", book.KindleBook{ASIN: "A"})
	seedBook(store.MemStore, "upcoming", book.KindleBook{ASIN: "B"})

	_, err := MergeUpcoming(context.Background(), store, "unprocessed", "upcoming", 3)
	if !errors.Is(err, errMutateFailure) {
		t.Errorf("err = %v, want sentinel", err)
	}
	// upcoming は消去されず残る。
	upcomingObj, _ := store.Get(context.Background(), "upcoming")
	records, _ := DecodeBooks(upcomingObj.Body)
	if len(records) != 1 || records[0].Book.ASIN != "B" {
		t.Errorf("upcoming must not be cleared on unprocessed Get failure: %+v", records)
	}
}

// TestMergeUpcoming_UpcomingGetErrorReturns は upcoming の Get が非 NotFound error のとき
// 即座に error を返すことを検証する。
func TestMergeUpcoming_UpcomingGetErrorReturns(t *testing.T) {
	store := &keyedGetErrStore{MemStore: NewMemStore(), failKey: "upcoming", getErr: errMutateFailure}

	_, err := MergeUpcoming(context.Background(), store, "unprocessed", "upcoming", 3)
	if !errors.Is(err, errMutateFailure) {
		t.Errorf("err = %v, want sentinel", err)
	}
}

// TestMergeUpcoming_RerunIsIdempotent は2回目実行で added=0 となり、
// unprocessed の内容が安定することを検証する（再実行安全性、SPECIFICATION.md 7.5）。
func TestMergeUpcoming_RerunIsIdempotent(t *testing.T) {
	store := NewMemStore()
	seedBook(store, "unprocessed", book.KindleBook{
		ASIN: "A", Title: "a", ReleaseDate: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	})
	seedBook(store, "upcoming", book.KindleBook{
		ASIN: "B", Title: "b", ReleaseDate: time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC),
	})

	first, err := MergeUpcoming(context.Background(), store, "unprocessed", "upcoming", 3)
	if err != nil {
		t.Fatalf("first MergeUpcoming: %v", err)
	}
	if first != 1 {
		t.Fatalf("first added = %d, want 1", first)
	}
	// 2回目: upcoming は空配列化済みのため added=0。
	second, err := MergeUpcoming(context.Background(), store, "unprocessed", "upcoming", 3)
	if err != nil {
		t.Fatalf("second MergeUpcoming: %v", err)
	}
	if second != 0 {
		t.Errorf("second added = %d, want 0 (idempotent)", second)
	}
	obj, _ := store.Get(context.Background(), "unprocessed")
	records, _ := DecodeBooks(obj.Body)
	if len(records) != 2 {
		t.Errorf("unprocessed len = %d, want 2 (stable on rerun)", len(records))
	}
}

// alwaysPreconditionStore は Put が常に前提不一致を返す検証用 store。
// clearUpcomingIfUnchanged の clear Put が 412 でも error にせず added を返すことを検証する。
type alwaysPreconditionStore struct {
	*MemStore
}

func (s *alwaysPreconditionStore) Put(_ context.Context, _ string, _ []byte, _ PutOptions) error {
	return ErrPreconditionFailed
}

// TestClearUpcomingIfUnchanged_ClearConflictReturnsAdded は upcoming の clear Put が
// 412 になった場合でも error にせず added を返し、upcoming を無理に消去しないことを検証する。
func TestClearUpcomingIfUnchanged_ClearConflictReturnsAdded(t *testing.T) {
	store := &alwaysPreconditionStore{MemStore: NewMemStore()}
	seedBook(store.MemStore, "upcoming", book.KindleBook{ASIN: "C"})
	obj, _ := store.Get(context.Background(), "upcoming")

	added, err := clearUpcomingIfUnchanged(context.Background(), store, "upcoming", obj.ETag, 1)
	if err != nil {
		t.Fatalf("clearUpcomingIfUnchanged on clear 412: %v", err)
	}
	if added != 1 {
		t.Errorf("added = %d, want 1 (clear conflict must not lose added count)", added)
	}
	// clear Put が失敗したため upcoming は元のままで残る。
	upcomingObj, _ := store.Get(context.Background(), "upcoming")
	records, _ := DecodeBooks(upcomingObj.Body)
	if len(records) != 1 {
		t.Errorf("upcoming must remain when clear Put fails: %+v", records)
	}
}

// dedupExtraOf は対象 ASIN のレコードから Extra の指定 key だけを取り出す（保持検証用）。
func dedupExtraOf(t *testing.T, records []BookRecord, asin, key string) json.RawMessage {
	t.Helper()
	idx := findBookIndex(records, asin)
	if idx == -1 {
		t.Fatalf("ASIN %s not found", asin)
	}
	return records[idx].Extra[key]
}

// countASIN は records 内の指定 ASIN 出現数を返す（重複残存検出用）。
func countASIN(records []BookRecord, asin string) int {
	n := 0
	for _, r := range records {
		if r.Book.ASIN == asin {
			n++
		}
	}
	return n
}

// TestUpsertBookRecord_DedupsExistingDuplicateASINs は upsert 前から同一 ASIN が重複していた入力に対し、
// 保存後に BookRecord 単位で ASIN 重複排除されることを検証する（SPECIFICATION.md 9.2）。
// 最初の出現レコードの Extra を含む全情報を保持し、別 ASIN は追加される。
func TestUpsertBookRecord_DedupsExistingDuplicateASINs(t *testing.T) {
	store := NewMemStore()
	store.Seed("k", `[
        {"ASIN":"B0DUP0000001","Title":"first","ReleaseDate":"2026-01-01T00:00:00Z","CurrentPrice":0,"MaxPrice":0,"URL":"","CreatedAt":"2026-01-01T00:00:00Z","Memo":"first"},
        {"ASIN":"B0DUP0000001","Title":"second","ReleaseDate":"2026-01-01T00:00:00Z","CurrentPrice":0,"MaxPrice":0,"URL":"","CreatedAt":"2026-01-01T00:00:00Z","Memo":"second"},
        {"ASIN":"B0KEEP000001","Title":"keep","ReleaseDate":"2026-02-02T00:00:00Z","CurrentPrice":0,"MaxPrice":0,"URL":"","CreatedAt":"2026-02-02T00:00:00Z"}
    ]`)

	if err := UpsertBookRecord(context.Background(), store, "k", BookRecord{Book: book.KindleBook{
		ASIN: "B0NEW0000001", Title: "new", ReleaseDate: time.Date(2026, 3, 3, 0, 0, 0, 0, time.UTC),
	}}); err != nil {
		t.Fatalf("UpsertBookRecord: %v", err)
	}
	obj, _ := store.Get(context.Background(), "k")
	records, _ := DecodeBooks(obj.Body)

	if got := countASIN(records, "B0DUP0000001"); got != 1 {
		t.Errorf("duplicate ASIN count = %d, want 1 (保存後に重複排除)", got)
	}
	// 最初の出現（Memo=first）の Extra が保持され、2件目（Memo=second）は失われる。
	if !bytes.Equal(dedupExtraOf(t, records, "B0DUP0000001", "Memo"), []byte(`"first"`)) {
		t.Errorf("first occurrence Extra not kept: %s", obj.Body)
	}
	// 別 ASIN はそのまま残り、新規 ASIN は追加される。
	if findBookIndex(records, "B0KEEP000001") == -1 || findBookIndex(records, "B0NEW0000001") == -1 {
		t.Errorf("other ASINs lost: %+v", asinOrder(records))
	}
	if len(records) != 3 {
		t.Errorf("len = %d, want 3 (dup 1件 + keep + new): %s", len(records), obj.Body)
	}
}

// TestUpsertBookRecord_UpdatesFirstAndDropsDuplicateOnTargetUpsert は対象 ASIN 自体が重複している場合、
// 最初の出現を更新して2件目以降を排除することを検証する。CreatedAt は最初の出現を保持する。
func TestUpsertBookRecord_UpdatesFirstAndDropsDuplicateOnTargetUpsert(t *testing.T) {
	store := NewMemStore()
	original := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	store.Seed("k", `[
        {"ASIN":"B0DUP0000001","Title":"first","ReleaseDate":"2026-01-01T00:00:00Z","CurrentPrice":0,"MaxPrice":0,"URL":"","CreatedAt":"2026-01-01T00:00:00Z","Memo":"first"},
        {"ASIN":"B0DUP0000001","Title":"second","ReleaseDate":"2026-01-01T00:00:00Z","CurrentPrice":0,"MaxPrice":0,"URL":"","CreatedAt":"2026-02-02T00:00:00Z","Memo":"second"}
    ]`)

	if err := UpsertBookRecord(context.Background(), store, "k", BookRecord{Book: book.KindleBook{
		ASIN: "B0DUP0000001", Title: "更新", ReleaseDate: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		CreatedAt: time.Date(2026, 9, 9, 0, 0, 0, 0, time.UTC),
	}}); err != nil {
		t.Fatalf("UpsertBookRecord: %v", err)
	}
	obj, _ := store.Get(context.Background(), "k")
	records, _ := DecodeBooks(obj.Body)

	if got := countASIN(records, "B0DUP0000001"); got != 1 {
		t.Errorf("duplicate ASIN count = %d, want 1", got)
	}
	idx := findBookIndex(records, "B0DUP0000001")
	if records[idx].Book.Title != "更新" {
		t.Errorf("title = %q, want 更新", records[idx].Book.Title)
	}
	// CreatedAt は最初の出現を保持し、upsert 側の値で上書きしない（SPECIFICATION.md 9.2/9.4）。
	if !records[idx].Book.CreatedAt.Equal(original) {
		t.Errorf("CreatedAt = %v, want %v (最初の出現を保持)", records[idx].Book.CreatedAt, original)
	}
	// Extra も最初の出現を保持する。
	if !bytes.Equal(records[idx].Extra["Memo"], []byte(`"first"`)) {
		t.Errorf("first Extra not kept: %s", obj.Body)
	}
}

// dupConflictStore は初回 Put 直前に同一 ASIN を2件含む最新本文を seed して 412 を起こす。
// retry で再読込した本文に重複がある場合でも保存前に重複排除されることを検証するための store。
type dupConflictStore struct {
	*MemStore
	conflicted bool
}

func (s *dupConflictStore) Put(ctx context.Context, key string, body []byte, opts PutOptions) error {
	if key == "k" && !s.conflicted {
		s.Seed("k", `[
            {"ASIN":"B0DUP0000001","Title":"first","ReleaseDate":"2026-01-01T00:00:00Z","CurrentPrice":0,"MaxPrice":0,"URL":"","CreatedAt":"2026-01-01T00:00:00Z","Memo":"first"},
            {"ASIN":"B0DUP0000001","Title":"second","ReleaseDate":"2026-01-01T00:00:00Z","CurrentPrice":0,"MaxPrice":0,"URL":"","CreatedAt":"2026-01-01T00:00:00Z","Memo":"second"}
        ]`)
		s.conflicted = true
		return ErrPreconditionFailed
	}
	return s.MemStore.Put(ctx, key, body, opts)
}

// TestMutateBooks_ConflictRetryDedupesLatestBody は ETag 競合で読み直した最新本文に同一 ASIN 重複が
// ある場合でも、保存前に BookRecord 単位で重複排除されることを検証する（SPECIFICATION.md 9.2/9.5）。
func TestMutateBooks_ConflictRetryDedupesLatestBody(t *testing.T) {
	store := &dupConflictStore{MemStore: NewMemStore()}
	store.Seed("k", `[{"ASIN":"B0DUP0000001","Title":"first","ReleaseDate":"2026-01-01T00:00:00Z","CurrentPrice":0,"MaxPrice":0,"URL":"","CreatedAt":"2026-01-01T00:00:00Z","Memo":"first"}]`)

	if err := UpsertBookRecord(context.Background(), store, "k", BookRecord{Book: book.KindleBook{
		ASIN: "B0NEW0000001", Title: "new", ReleaseDate: time.Date(2026, 3, 3, 0, 0, 0, 0, time.UTC),
	}}); err != nil {
		t.Fatalf("UpsertBookRecord: %v", err)
	}
	obj, _ := store.Get(context.Background(), "k")
	records, _ := DecodeBooks(obj.Body)

	if got := countASIN(records, "B0DUP0000001"); got != 1 {
		t.Errorf("duplicate ASIN count = %d, want 1 (競合後の最新本文重複も排除)", got)
	}
	if !bytes.Equal(dedupExtraOf(t, records, "B0DUP0000001", "Memo"), []byte(`"first"`)) {
		t.Errorf("first occurrence Extra not kept after retry: %s", obj.Body)
	}
	if findBookIndex(records, "B0NEW0000001") == -1 {
		t.Errorf("upsert target lost after retry: %+v", asinOrder(records))
	}
}

// TestMergeUpcoming_DedupsWithinUnprocessed は unprocessed 側に同一 ASIN 重複がある場合でも
// 統合保存後に重複排除されることを検証する（SPECIFICATION.md 9.2/10）。
func TestMergeUpcoming_DedupsWithinUnprocessed(t *testing.T) {
	store := NewMemStore()
	store.Seed("unprocessed", `[
        {"ASIN":"B0DUP0000001","Title":"first","ReleaseDate":"2026-01-01T00:00:00Z","CurrentPrice":0,"MaxPrice":0,"URL":"","CreatedAt":"2026-01-01T00:00:00Z","Memo":"first"},
        {"ASIN":"B0DUP0000001","Title":"second","ReleaseDate":"2026-01-01T00:00:00Z","CurrentPrice":0,"MaxPrice":0,"URL":"","CreatedAt":"2026-01-01T00:00:00Z","Memo":"second"}
    ]`)
	seedBook(store, "upcoming", book.KindleBook{ASIN: "B0NEW", Title: "new", ReleaseDate: time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)})

	if _, err := MergeUpcoming(context.Background(), store, "unprocessed", "upcoming", 3); err != nil {
		t.Fatalf("MergeUpcoming: %v", err)
	}
	obj, _ := store.Get(context.Background(), "unprocessed")
	records, _ := DecodeBooks(obj.Body)

	if got := countASIN(records, "B0DUP0000001"); got != 1 {
		t.Errorf("duplicate ASIN count = %d, want 1", got)
	}
	if !bytes.Equal(dedupExtraOf(t, records, "B0DUP0000001", "Memo"), []byte(`"first"`)) {
		t.Errorf("first occurrence Extra not kept: %s", obj.Body)
	}
	if findBookIndex(records, "B0NEW") == -1 {
		t.Errorf("upcoming B0NEW not merged: %+v", asinOrder(records))
	}
}

// TestMergeUpcoming_MissingUnprocessedErrors は unprocessed が存在しない場合に空配列へ fallback せず
// error を返し新規作成もしないことを検証する（SPECIFICATION.md 9.1）。
func TestMergeUpcoming_MissingUnprocessedErrors(t *testing.T) {
	store := NewMemStore()
	seedBook(store, "upcoming", book.KindleBook{ASIN: "B0NEW", Title: "new", ReleaseDate: time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)})

	_, err := MergeUpcoming(context.Background(), store, "unprocessed", "upcoming", 3)
	if !errors.Is(err, ErrObjectNotFound) {
		t.Fatalf("err = %v, want wrap of ErrObjectNotFound", err)
	}
	if _, err := store.Get(context.Background(), "unprocessed"); !errors.Is(err, ErrObjectNotFound) {
		t.Errorf("unprocessed must not be created on missing: %v", err)
	}
	upcomingObj, _ := store.Get(context.Background(), "upcoming")
	if strings.TrimSpace(string(upcomingObj.Body)) == "[]" {
		t.Errorf("upcoming must not be cleared when unprocessed is missing: %s", upcomingObj.Body)
	}
}

// TestMergeUpcoming_MissingUpcomingErrors は upcoming が存在しない場合に空配列へ fallback せず
// error を返すことを検証する（SPECIFICATION.md 9.1）。upcoming は空配列状態だけを正常とする。
func TestMergeUpcoming_MissingUpcomingErrors(t *testing.T) {
	store := NewMemStore()
	seedBook(store, "unprocessed", book.KindleBook{ASIN: "B0A", Title: "a", ReleaseDate: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)})

	_, err := MergeUpcoming(context.Background(), store, "unprocessed", "upcoming", 3)
	if !errors.Is(err, ErrObjectNotFound) {
		t.Fatalf("err = %v, want wrap of ErrObjectNotFound", err)
	}
}

// deleteUpcomingOnUnprocessedPutStore は unprocessed への Put 成功後に upcoming を削除する検証用 store。
// MergeUpcoming で Unprocessed への merge が commit された後、clear 直前に upcoming_asins.json が
// 手動削除・rename 相当で消失した状況を再現し、非 transaction 契約と再実行時 reconcile を検証する。
// upcomingDeleted で最初の1回だけ削除し、再実行で upcoming を復元した後は消失させない。
type deleteUpcomingOnUnprocessedPutStore struct {
	*MemStore
	upcomingDeleted bool
}

func (s *deleteUpcomingOnUnprocessedPutStore) Put(_ context.Context, key string, body []byte, opts PutOptions) error {
	if err := s.MemStore.Put(context.Background(), key, body, opts); err != nil {
		return err
	}
	if key == "unprocessed" && !s.upcomingDeleted {
		// テスト単スレッドのため lock なしで map を直接操作する（本番コードではない）。
		delete(s.objects, "upcoming")
		s.upcomingDeleted = true
	}
	return nil
}

// TestClearUpcomingIfUnchanged_MissingObjectErrors は upcoming が clear 直前に存在しない場合、
// 成功扱い（added 返却）せず Get error を返すことを検証する（SPECIFICATION.md 9.1 の存在必須 object 契約）。
// ErrObjectNotFound を ETag 変更・412 と同一視せず、object 欠落を失敗とする回帰保護。
func TestClearUpcomingIfUnchanged_MissingObjectErrors(t *testing.T) {
	store := NewMemStore()
	// upcoming は存在しない（必須 object の欠落）。

	added, err := clearUpcomingIfUnchanged(context.Background(), store, "upcoming", "start-etag", 2)
	if err == nil {
		t.Fatalf("err = nil, want error for missing required object (got added=%d)", added)
	}
	if !errors.Is(err, ErrObjectNotFound) {
		t.Errorf("err = %v, want wrap of ErrObjectNotFound", err)
	}
}

// TestMergeUpcoming_UpcomingDeletedAfterMergeErrorsAndKeepsUnprocessed は Unprocessed への merge 成功後、
// clear 直前に Upcoming が削除された場合、error を返しつつ Unprocessed の merge 結果は保持されること
// （SPECIFICATION.md 7.5 の非 transaction 契約）を検証する。silent fallback せず clear 段階の失敗を表面化する。
func TestMergeUpcoming_UpcomingDeletedAfterMergeErrorsAndKeepsUnprocessed(t *testing.T) {
	store := &deleteUpcomingOnUnprocessedPutStore{MemStore: NewMemStore()}
	seedBook(store.MemStore, "unprocessed", book.KindleBook{
		ASIN: "A", Title: "a", ReleaseDate: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	})
	seedBook(store.MemStore, "upcoming", book.KindleBook{
		ASIN: "B", Title: "b", ReleaseDate: time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC),
	})

	_, err := MergeUpcoming(context.Background(), store, "unprocessed", "upcoming", 3)
	if err == nil {
		t.Fatal("err = nil, want error when upcoming missing at clear stage")
	}
	if !errors.Is(err, ErrObjectNotFound) {
		t.Errorf("err = %v, want wrap of ErrObjectNotFound", err)
	}
	// Unprocessed への merge は commit 済みで巻き戻らない（非 transaction）。
	obj, _ := store.Get(context.Background(), "unprocessed")
	records, _ := DecodeBooks(obj.Body)
	if findBookIndex(records, "B") == -1 {
		t.Errorf("Unprocessed への merge 結果が巻き戻った（非 transaction 契約違反）: %+v", asinOrder(records))
	}
}

// TestMergeUpcoming_RerunAfterClearErrorReconciles は clear 段階の Upcoming 欠落 error 後、
// Upcoming を復元して再実行すると merge が冪等に補完され（重複せず）clear が完了することを検証する
// （SPECIFICATION.md 7.5/10 の非 transaction reconcile 方針）。
func TestMergeUpcoming_RerunAfterClearErrorReconciles(t *testing.T) {
	store := &deleteUpcomingOnUnprocessedPutStore{MemStore: NewMemStore()}
	seedBook(store.MemStore, "unprocessed", book.KindleBook{
		ASIN: "A", Title: "a", ReleaseDate: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	})
	seedBook(store.MemStore, "upcoming", book.KindleBook{
		ASIN: "B", Title: "b", ReleaseDate: time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC),
	})

	// 1回目: merge 成功後、clear 段階で Upcoming 欠落により error。
	if _, err := MergeUpcoming(context.Background(), store, "unprocessed", "upcoming", 3); err == nil {
		t.Fatal("first run should error when upcoming deleted at clear stage")
	}

	// 運用での object 復元に相当: Upcoming を空配列で再作成。
	seedBook(store.MemStore, "upcoming")

	// 2回目: merge は冪等（B は既に Unprocessed にあるため added=0）、clear は成功。
	added, err := MergeUpcoming(context.Background(), store, "unprocessed", "upcoming", 3)
	if err != nil {
		t.Fatalf("second MergeUpcoming: %v", err)
	}
	if added != 0 {
		t.Errorf("second added = %d, want 0 (idempotent merge, no duplication)", added)
	}

	// Unprocessed は A, B の2件で安定（重複しない）。
	obj, _ := store.Get(context.Background(), "unprocessed")
	records, _ := DecodeBooks(obj.Body)
	if len(records) != 2 || countASIN(records, "B") != 1 {
		t.Errorf("unprocessed not stable on rerun: %+v", asinOrder(records))
	}
	// Upcoming は空配列化されている（clear 完了）。
	upcomingObj, _ := store.Get(context.Background(), "upcoming")
	if strings.TrimSpace(string(upcomingObj.Body)) != "[]" {
		t.Errorf("upcoming not cleared on rerun: %s", upcomingObj.Body)
	}
}
