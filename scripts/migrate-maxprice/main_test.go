package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/shinderuman/kindle-automation/internal/domain/book"
	"github.com/shinderuman/kindle-automation/internal/storage"
)

// encodeBooks はテスト用に BookRecord 一覧を本番 codec で JSON へ変換する。
func encodeBooks(t *testing.T, records []storage.BookRecord) []byte {
	t.Helper()
	body, err := storage.EncodeBooks(records)
	if err != nil {
		t.Fatalf("encode books: %v", err)
	}
	return body
}

func TestApplyMigration(t *testing.T) {
	// MaxPrice へ紙書籍価格が入っている典型的な移行対象のレコード。
	updated := storage.BookRecord{Book: book.KindleBook{
		ASIN: "B000000001", Title: "タイトル1",
		CurrentPrice: book.NewPrice(759), MaxPrice: book.NewPrice(792),
	}}
	// CurrentPrice==0 のレコード。SPECIFICATION.md 20.2 で MaxPrice も 0 になる。
	currentZero := storage.BookRecord{Book: book.KindleBook{
		ASIN: "B000000002", Title: "タイトル2",
		CurrentPrice: book.Price{}, MaxPrice: book.NewPrice(1500),
	}}
	// 既に MaxPrice==CurrentPrice で変更不要なレコード。
	unchanged := storage.BookRecord{Book: book.KindleBook{
		ASIN: "B000000003", Title: "タイトル3",
		CurrentPrice: book.NewPrice(759), MaxPrice: book.NewPrice(759),
	}}

	t.Run("MaxPriceをCurrentPriceへ書き換え件数順序を保持", func(t *testing.T) {
		in := []storage.BookRecord{updated, currentZero, unchanged}
		got, changed, currentZeroCount := applyMigration(in)

		if len(got) != len(in) {
			t.Fatalf("件数が変わった: %d -> %d", len(in), len(got))
		}
		if changed != 2 {
			t.Errorf("changed = %d, want 2", changed)
		}
		if currentZeroCount != 1 {
			t.Errorf("current_zero = %d, want 1", currentZeroCount)
		}
		for i := range in {
			if got[i].Book.ASIN != in[i].Book.ASIN {
				t.Errorf("順序が変わった at %d: %s -> %s", i, in[i].Book.ASIN, got[i].Book.ASIN)
			}
		}
	})

	t.Run("CurrentPrice0はMaxPriceも未取得になる", func(t *testing.T) {
		got, _, _ := applyMigration([]storage.BookRecord{currentZero})
		if got[0].Book.MaxPrice.Valid() {
			t.Errorf("CurrentPrice==0 の MaxPrice.Valid = true, want false")
		}
		if got[0].Book.MaxPrice.Yen() != 0 {
			t.Errorf("CurrentPrice==0 の MaxPrice.Yen = %v, want 0", got[0].Book.MaxPrice.Yen())
		}
	})

	t.Run("CurrentPriceは変更しない", func(t *testing.T) {
		got, _, _ := applyMigration([]storage.BookRecord{updated})
		if !priceEqual(got[0].Book.CurrentPrice, book.NewPrice(759)) {
			t.Errorf("CurrentPrice が変わった: %v", got[0].Book.CurrentPrice)
		}
		if !priceEqual(got[0].Book.MaxPrice, got[0].Book.CurrentPrice) {
			t.Errorf("MaxPrice != CurrentPrice")
		}
	})

	t.Run("既に等しければchangedに数えない", func(t *testing.T) {
		_, changed, _ := applyMigration([]storage.BookRecord{unchanged})
		if changed != 0 {
			t.Errorf("changed = %d, want 0", changed)
		}
	})
}

func TestMigrateObject_DryRun(t *testing.T) {
	ctx := context.Background()
	before := []storage.BookRecord{
		{Book: book.KindleBook{ASIN: "B000000001", CurrentPrice: book.NewPrice(759), MaxPrice: book.NewPrice(792)}},
		{Book: book.KindleBook{ASIN: "B000000002", CurrentPrice: book.Price{}, MaxPrice: book.NewPrice(1500)}},
	}
	store := storage.NewMemStore()
	store.Seed("unprocessed_asins.json", string(encodeBooks(t, before)))
	wantBody := encodeBooks(t, before)

	rep, err := migrateObject(ctx, store, "unprocessed_asins.json", false)
	if err != nil {
		t.Fatalf("migrateObject dry-run: %v", err)
	}
	if rep.Total != 2 || rep.Changed != 2 || rep.CurrentZero != 1 {
		t.Errorf("report = %+v, want total=2 changed=2 current_zero=1", rep)
	}

	// dry-run は S3 へ書き込まない。
	obj, err := store.Get(ctx, "unprocessed_asins.json")
	if err != nil {
		t.Fatalf("get after dry-run: %v", err)
	}
	if !bytes.Equal(obj.Body, wantBody) {
		t.Errorf("dry-run が本文を書き換えた")
	}
}

func TestMigrateObject_Apply(t *testing.T) {
	ctx := context.Background()
	release := time.Date(2026, 8, 28, 0, 0, 0, 0, time.UTC)
	created := time.Date(2026, 7, 17, 0, 0, 0, 0, time.UTC)
	before := []storage.BookRecord{
		{
			Book: book.KindleBook{
				ASIN: "B000000001", Title: "タイトル1", ReleaseDate: release, CreatedAt: created,
				CurrentPrice: book.NewPrice(759), MaxPrice: book.NewPrice(792),
				URL: "https://www.amazon.co.jp/dp/B000000001?tag=test-22",
			},
			Extra: map[string]json.RawMessage{"Memo": json.RawMessage(`"手動メモ"`)},
		},
	}
	store := storage.NewMemStore()
	store.Seed("unprocessed_asins.json", string(encodeBooks(t, before)))

	rep, err := migrateObject(ctx, store, "unprocessed_asins.json", true)
	if err != nil {
		t.Fatalf("migrateObject apply: %v", err)
	}
	if rep.Changed != 1 {
		t.Errorf("changed = %d, want 1", rep.Changed)
	}

	obj, err := store.Get(ctx, "unprocessed_asins.json")
	if err != nil {
		t.Fatalf("get after apply: %v", err)
	}
	after, err := storage.DecodeBooks(obj.Body)
	if err != nil {
		t.Fatalf("decode after apply: %v", err)
	}
	if len(after) != 1 {
		t.Fatalf("件数が変わった: %d", len(after))
	}
	if !priceEqual(after[0].Book.MaxPrice, book.NewPrice(759)) {
		t.Errorf("MaxPrice = %v, want 759", after[0].Book.MaxPrice)
	}
	// CurrentPrice・他 field は不変。
	if !priceEqual(after[0].Book.CurrentPrice, book.NewPrice(759)) {
		t.Errorf("CurrentPrice が変わった: %v", after[0].Book.CurrentPrice)
	}
	if after[0].Book.Title != "タイトル1" || after[0].Book.URL != before[0].Book.URL {
		t.Errorf("Title/URL が変わった: %+v", after[0].Book)
	}
	if !after[0].Book.ReleaseDate.Equal(release) || !after[0].Book.CreatedAt.Equal(created) {
		t.Errorf("ReleaseDate/CreatedAt が変わった: %+v", after[0].Book)
	}
	// 未知 field も保持する。
	if !extraEqual(before[0].Extra, after[0].Extra) {
		t.Errorf("Extra が変わった: %v -> %v", before[0].Extra, after[0].Extra)
	}
}

// applyStore は Get と Put を記録する検証用 ObjectStore。If-Match の引き渡しを確認する。
type applyStore struct {
	body     []byte
	etag     string
	putOpts  storage.PutOptions
	putCalls int
}

func (s *applyStore) Get(_ context.Context, _ string) (storage.Object, error) {
	return storage.Object{Body: s.body, ETag: s.etag}, nil
}

func (s *applyStore) Put(_ context.Context, _ string, _ []byte, opts storage.PutOptions) error {
	s.putOpts = opts
	s.putCalls++
	return nil
}

func TestMigrateObject_ApplyUsesIfMatch(t *testing.T) {
	ctx := context.Background()
	body := encodeBooks(t, []storage.BookRecord{
		{Book: book.KindleBook{ASIN: "B000000001", CurrentPrice: book.NewPrice(759), MaxPrice: book.NewPrice(792)}},
	})
	store := &applyStore{body: body, etag: "etag-123"}

	if _, err := migrateObject(ctx, store, "unprocessed_asins.json", true); err != nil {
		t.Fatalf("migrateObject apply: %v", err)
	}
	if store.putCalls != 1 {
		t.Errorf("putCalls = %d, want 1", store.putCalls)
	}
	if store.putOpts.IfMatch != "etag-123" {
		t.Errorf("IfMatch = %q, want etag-123", store.putOpts.IfMatch)
	}
}

func TestMigrateObject_DryRunDoesNotPut(t *testing.T) {
	ctx := context.Background()
	body := encodeBooks(t, []storage.BookRecord{
		{Book: book.KindleBook{ASIN: "B000000001", CurrentPrice: book.NewPrice(759), MaxPrice: book.NewPrice(792)}},
	})
	store := &applyStore{body: body, etag: "etag-123"}

	if _, err := migrateObject(ctx, store, "unprocessed_asins.json", false); err != nil {
		t.Fatalf("migrateObject dry-run: %v", err)
	}
	if store.putCalls != 0 {
		t.Errorf("dry-run が Put を呼んだ: %d", store.putCalls)
	}
}

func TestMigrateObject_NotFound(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemStore() // 空

	_, err := migrateObject(ctx, store, "notified_asins.json", false)
	if err == nil {
		t.Fatal("object 不存在で error がない")
	}
	if !errors.Is(err, storage.ErrObjectNotFound) {
		t.Errorf("err = %v, want ErrObjectNotFound を含む", err)
	}
}

func TestValidateInvariants_DetectsCountChange(t *testing.T) {
	before := []storage.BookRecord{
		{Book: book.KindleBook{ASIN: "B000000001", CurrentPrice: book.NewPrice(759), MaxPrice: book.NewPrice(759)}},
	}
	after := []storage.BookRecord{} // 件数が減った破壊状態
	if err := validateInvariants(before, after, "k"); err == nil {
		t.Fatal("件数変化を検出しなかった")
	}
}

func TestValidateInvariants_DetectsCurrentPriceChange(t *testing.T) {
	before := []storage.BookRecord{
		{Book: book.KindleBook{ASIN: "B000000001", CurrentPrice: book.NewPrice(759), MaxPrice: book.NewPrice(759)}},
	}
	after := []storage.BookRecord{
		{Book: book.KindleBook{ASIN: "B000000001", CurrentPrice: book.NewPrice(999), MaxPrice: book.NewPrice(999)}},
	}
	if err := validateInvariants(before, after, "k"); err == nil {
		t.Fatal("CurrentPrice 変化を検出しなかった")
	}
}

// TestMigrateObject_ApplyThenRerunNoChange は apply 後に再度 dry-run すると
// changed=0 になる（再実行冪等性）ことを検証する（SPECIFICATION.md 20.2 一度だけ適用）。
func TestMigrateObject_ApplyThenRerunNoChange(t *testing.T) {
	ctx := context.Background()
	before := []storage.BookRecord{
		{Book: book.KindleBook{ASIN: "B000000001", CurrentPrice: book.NewPrice(759), MaxPrice: book.NewPrice(792)}},
		{Book: book.KindleBook{ASIN: "B000000002", CurrentPrice: book.Price{}, MaxPrice: book.NewPrice(1500)}},
	}
	store := storage.NewMemStore()
	store.Seed("unprocessed_asins.json", string(encodeBooks(t, before)))

	if _, err := migrateObject(ctx, store, "unprocessed_asins.json", true); err != nil {
		t.Fatalf("first apply: %v", err)
	}

	// 2回目は dry-run。MaxPrice==CurrentPrice になっているため changed=0。
	rep, err := migrateObject(ctx, store, "unprocessed_asins.json", false)
	if err != nil {
		t.Fatalf("rerun dry-run: %v", err)
	}
	if rep.Changed != 0 {
		t.Errorf("rerun changed = %d, want 0 (idempotent): %+v", rep.Changed, rep)
	}
	if rep.Total != 2 {
		t.Errorf("rerun total = %d, want 2", rep.Total)
	}
}

func TestSplitKeys(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"unprocessed_asins.json,upcoming_asins.json,notified_asins.json",
			[]string{"unprocessed_asins.json", "upcoming_asins.json", "notified_asins.json"}},
		{" a , ,b ", []string{"a", "b"}},
		{"", []string{}},
	}
	for _, c := range cases {
		got := splitKeys(c.in)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("splitKeys(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}
