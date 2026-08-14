package storage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

func asinOrder(records []BookRecord) []string {
	out := make([]string, len(records))
	for i, r := range records {
		out[i] = r.Book.ASIN
	}
	return out
}

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
	store.Seed("k", `[
        {"ASIN":"B0OLDEST001","Title":"Z","ReleaseDate":"2025-01-01T00:00:00Z","CurrentPrice":0,"MaxPrice":0,"URL":"","CreatedAt":"2025-01-01T00:00:00Z"},
        {"ASIN":"B0SAME000001","Title":"BBB","ReleaseDate":"2026-03-03T00:00:00Z","CurrentPrice":0,"MaxPrice":0,"URL":"","CreatedAt":"2026-03-03T00:00:00Z","Memo":"手動"},
        {"ASIN":"B0SAME000002","Title":"AAA","ReleaseDate":"2026-03-03T00:00:00Z","CurrentPrice":0,"MaxPrice":0,"URL":"","CreatedAt":"2026-03-03T00:00:00Z"}
    ]`)
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
	if _, ok := records[findBookIndex(records, "B0SAME000001")].Extra["Memo"]; !ok {
		t.Errorf("unknown field Memo lost after sort: %s", obj.Body)
	}
}

func TestUpsertBookRecordChanged_ReportsChangedForAddAndModify(t *testing.T) {
	store := NewMemStore()
	store.Seed("k", `[]`)
	target := BookRecord{Book: book.KindleBook{
		ASIN: "B0FX3X569X", Title: "T", URL: "https://u",
		ReleaseDate:  time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC),
		CurrentPrice: book.NewPrice(800), MaxPrice: book.NewPrice(800),
		CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}}

	changed, err := UpsertBookRecordChanged(context.Background(), store, "k", target)
	if err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	if !changed {
		t.Errorf("new ASIN must report changed=true")
	}

	changed, err = UpsertBookRecordChanged(context.Background(), store, "k", BookRecord{Book: book.KindleBook{
		ASIN: "B0FX3X569X", Title: "T", URL: "https://u",
		ReleaseDate:  time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC),
		CurrentPrice: book.NewPrice(800), MaxPrice: book.NewPrice(800),
		CreatedAt: time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC),
	}})
	if err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	if changed {
		t.Errorf("unchanged re-upsert must report changed=false")
	}

	changed, err = UpsertBookRecordChanged(context.Background(), store, "k", BookRecord{Book: book.KindleBook{
		ASIN: "B0FX3X569X", Title: "T", URL: "https://u",
		ReleaseDate:  time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC),
		CurrentPrice: book.NewPrice(900), MaxPrice: book.NewPrice(900),
	}})
	if err != nil {
		t.Fatalf("third upsert: %v", err)
	}
	if !changed {
		t.Errorf("price change must report changed=true")
	}
}

func TestUpsertBookRecordChanged_KeepsExistingExtraAndCreatedAt(t *testing.T) {
	store := NewMemStore()
	store.Seed("k", `[{"ASIN":"B0FX3X569X","Title":"T","ReleaseDate":"2026-01-01T00:00:00Z","CurrentPrice":0,"MaxPrice":0,"URL":"","CreatedAt":"2026-01-01T00:00:00Z","Memo":"手動"}]`)

	changed, err := UpsertBookRecordChanged(context.Background(), store, "k", BookRecord{Book: book.KindleBook{
		ASIN: "B0FX3X569X", Title: "T2",
		CurrentPrice: book.NewPrice(800), MaxPrice: book.NewPrice(800),
	}})
	if err != nil {
		t.Fatalf("UpsertBookRecordChanged: %v", err)
	}
	if !changed {
		t.Errorf("Title change must report changed=true")
	}
	obj, _ := store.Get(context.Background(), "k")
	records, _ := DecodeBooks(obj.Body)
	r := records[findBookIndex(records, "B0FX3X569X")]
	if _, ok := r.Extra["Memo"]; !ok {
		t.Errorf("unknown field Memo lost: %s", obj.Body)
	}
	if !r.Book.CreatedAt.Equal(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("CreatedAt not preserved: %v", r.Book.CreatedAt)
	}
}

// failStore は Get/Put で非前提不一致（非412）error を返し、それが retry されず即時伝播することを検証する（SPECIFICATION.md 9.5）。
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

// ctxErrStore は MemStore が ctx を無視するため ctx.Err() を伝える fake で cancel 伝播を検証する。
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

// errMutateFailure は即時伝播テストで「retry で再呼び出しされない」ことを確認するための固有 error。
var errMutateFailure = errors.New("mutate failure sentinel")

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

// optsCaptureStore は Put の opts を記録し、既存 object 更新で常に If-Match が設定される（無条件上書き経路がない）ことを検証する。
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

func dedupExtraOf(t *testing.T, records []BookRecord, asin, key string) json.RawMessage {
	t.Helper()
	idx := findBookIndex(records, asin)
	if idx == -1 {
		t.Fatalf("ASIN %s not found", asin)
	}
	return records[idx].Extra[key]
}

func countASIN(records []BookRecord, asin string) int {
	n := 0
	for _, r := range records {
		if r.Book.ASIN == asin {
			n++
		}
	}
	return n
}

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
	if !bytes.Equal(dedupExtraOf(t, records, "B0DUP0000001", "Memo"), []byte(`"first"`)) {
		t.Errorf("first occurrence Extra not kept: %s", obj.Body)
	}
	if findBookIndex(records, "B0KEEP000001") == -1 || findBookIndex(records, "B0NEW0000001") == -1 {
		t.Errorf("other ASINs lost: %+v", asinOrder(records))
	}
	if len(records) != 3 {
		t.Errorf("len = %d, want 3 (dup 1件 + keep + new): %s", len(records), obj.Body)
	}
}

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
	if !records[idx].Book.CreatedAt.Equal(original) {
		t.Errorf("CreatedAt = %v, want %v (最初の出現を保持)", records[idx].Book.CreatedAt, original)
	}
	if !bytes.Equal(records[idx].Extra["Memo"], []byte(`"first"`)) {
		t.Errorf("first Extra not kept: %s", obj.Body)
	}
}

// dupConflictStore は初回 Put 直前に同一 ASIN を2件含む本文を seed して 412 を起こし、retry で読み直した重複本文も保存前に排除されることを検証する。
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
