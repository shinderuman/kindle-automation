package storage

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/shinderuman/kindle-automation/internal/domain/book"
)

var testNow = time.Date(2026, 8, 9, 0, 0, 0, 0, time.UTC)
var futureRelease = time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC)
var pastRelease = time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)

func TestBookFileStore_UpdateOneBook_UpdatesAndKeepsExtra(t *testing.T) {
	store := NewMemStore()
	seedBook(store, "k", book.KindleBook{ASIN: "B0TARGET001", Title: "旧", CurrentPrice: book.NewPrice(700), MaxPrice: book.NewPrice(700)})
	store.Seed("k", `[{"ASIN":"B0TARGET001","Title":"旧","ReleaseDate":"2026-12-31T00:00:00Z","CurrentPrice":700,"MaxPrice":700,"URL":"","CreatedAt":"2026-01-01T00:00:00Z","Memo":"手動"}]`)

	s := NewBookFileStore(store, "k")
	applied, err := s.UpdateOneBook(context.Background(), "B0TARGET001", func(b book.KindleBook) book.KindleBook {
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
	if !strings.Contains(string(obj.Body), "手動") {
		t.Errorf("unknown field Memo lost: %s", obj.Body)
	}
	if !strings.Contains(string(obj.Body), "新") {
		t.Errorf("title not updated: %s", obj.Body)
	}
}

func TestBookFileStore_UpdateOneBook_TargetRemoved(t *testing.T) {
	store := NewMemStore()
	seedBook(store, "k", book.KindleBook{ASIN: "B0KEEP000001"})
	s := NewBookFileStore(store, "k")

	applied, err := s.UpdateOneBook(context.Background(), "B0REMOVED001", func(b book.KindleBook) book.KindleBook {
		t.Fatal("update called for removed target")
		return b
	})
	if err != nil {
		t.Fatalf("UpdateOneBook: %v", err)
	}
	if applied {
		t.Errorf("applied = true, want false for removed target")
	}
}

func TestBookFileStore_Upsert_IdempotentAndMerges(t *testing.T) {
	store := NewMemStore()
	store.Seed("k", `[]`)
	s := NewBookFileStore(store, "k")
	first := book.KindleBook{ASIN: "B0FX3X569X", Title: "初版", ReleaseDate: futureRelease, CreatedAt: testNow}

	if err := s.Upsert(context.Background(), first); err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	second := book.KindleBook{ASIN: "B0FX3X569X", Title: "改訂", ReleaseDate: futureRelease, CreatedAt: testNow}
	if err := s.Upsert(context.Background(), second); err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	obj, _ := store.Get(context.Background(), "k")
	records, _ := DecodeBooks(obj.Body)
	if len(records) != 1 {
		t.Fatalf("len = %d, want 1 (idempotent)", len(records))
	}
	if records[0].Book.Title != "改訂" {
		t.Errorf("title = %q, want 改訂 (merged)", records[0].Book.Title)
	}
}

func TestBookFileStore_Upsert_KeepsExtra(t *testing.T) {
	store := NewMemStore()
	store.Seed("k", `[{"ASIN":"B0FX3X569X","Title":"旧","ReleaseDate":"2026-12-31T00:00:00Z","CurrentPrice":0,"MaxPrice":0,"URL":"","CreatedAt":"2026-01-01T00:00:00Z","Memo":"手動"}]`)
	s := NewBookFileStore(store, "k")

	if err := s.Upsert(context.Background(), book.KindleBook{ASIN: "B0FX3X569X", Title: "新", ReleaseDate: futureRelease, CreatedAt: testNow}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	obj, _ := store.Get(context.Background(), "k")
	body := string(obj.Body)
	if !strings.Contains(body, "手動") {
		t.Errorf("unknown field Memo lost on upsert: %s", body)
	}
	if !strings.Contains(body, "新") {
		t.Errorf("title not merged on upsert: %s", body)
	}
	records, _ := DecodeBooks(obj.Body)
	if len(records) != 1 {
		t.Errorf("len = %d, want 1 (idempotent)", len(records))
	}
}

func TestBookFileStore_Upsert_PreservesCreatedAt(t *testing.T) {
	store := NewMemStore()
	store.Seed("k", `[]`)
	s := NewBookFileStore(store, "k")
	original := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	first := book.KindleBook{ASIN: "B0FX3X569X", Title: "初版", ReleaseDate: futureRelease, CreatedAt: original}

	if err := s.Upsert(context.Background(), first); err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	later := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	second := book.KindleBook{ASIN: "B0FX3X569X", Title: "改訂", ReleaseDate: futureRelease, CreatedAt: later}
	if err := s.Upsert(context.Background(), second); err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	obj, _ := store.Get(context.Background(), "k")
	records, _ := DecodeBooks(obj.Body)
	if len(records) != 1 {
		t.Fatalf("len = %d, want 1 (idempotent)", len(records))
	}
	if !records[0].Book.CreatedAt.Equal(original) {
		t.Errorf("CreatedAt = %v, want %v (preserved on update)", records[0].Book.CreatedAt, original)
	}
	if records[0].Book.Title != "改訂" {
		t.Errorf("title = %q, want 改訂 (merged)", records[0].Book.Title)
	}
}

func TestBookFileStore_Exists(t *testing.T) {
	store := NewMemStore()
	seedBook(store, "k", book.KindleBook{ASIN: "B0FX3X569X"})
	s := NewBookFileStore(store, "k")

	exists, err := s.Exists(context.Background(), "B0FX3X569X")
	if err != nil {
		t.Fatalf("Exists: %v", err)
	}
	if !exists {
		t.Errorf("Exists = false, want true")
	}
	exists, _ = s.Exists(context.Background(), "B0NOTFOUND01")
	if exists {
		t.Errorf("Exists for absent ASIN = true, want false")
	}
}

func TestBookFileStore_ApplyRetentionAndExists(t *testing.T) {
	store := NewMemStore()
	store.Seed("notified", `[
        {"ASIN":"B0FUTURE001","Title":"未来","ReleaseDate":"2026-12-31T00:00:00Z","CurrentPrice":0,"MaxPrice":0,"URL":"","CreatedAt":"2026-01-01T00:00:00Z"},
        {"ASIN":"B0PAST000001","Title":"過去","ReleaseDate":"2020-01-01T00:00:00Z","CurrentPrice":0,"MaxPrice":0,"URL":"","CreatedAt":"2020-01-01T00:00:00Z"}
    ]`)
	s := NewBookFileStore(store, "notified")

	exists, err := s.ApplyRetentionAndExists(context.Background(), "B0PAST000001", testNow)
	if err != nil {
		t.Fatalf("ApplyRetentionAndExists: %v", err)
	}
	if !exists {
		t.Errorf("過去分は処理開始時に存在する（alreadyNotified=true）")
	}

	obj, _ := store.Get(context.Background(), "notified")
	if strings.Contains(string(obj.Body), "B0PAST000001") {
		t.Errorf("過去分は保存期間適用で削除される: %s", obj.Body)
	}
	if !strings.Contains(string(obj.Body), "B0FUTURE001") {
		t.Errorf("将来分は残る: %s", obj.Body)
	}

	exists, _ = s.ApplyRetentionAndExists(context.Background(), "B0FUTURE001", testNow)
	if !exists {
		t.Errorf("将来分は存在する")
	}
}

func TestBookFileStore_Delete(t *testing.T) {
	store := NewMemStore()
	seedBook(store, "k", book.KindleBook{ASIN: "B0TARGET001"}, book.KindleBook{ASIN: "B0KEEP000001"})
	s := NewBookFileStore(store, "k")

	if err := s.Delete(context.Background(), "B0TARGET001"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	obj, _ := store.Get(context.Background(), "k")
	records, _ := DecodeBooks(obj.Body)
	if len(records) != 1 || records[0].Book.ASIN != "B0KEEP000001" {
		t.Errorf("delete failed: %+v", records)
	}
	if err := s.Delete(context.Background(), "B0TARGET001"); err != nil {
		t.Errorf("idempotent delete: %v", err)
	}
}

func TestBookFileStore_Book(t *testing.T) {
	store := NewMemStore()
	seedBook(store, "k", book.KindleBook{ASIN: "B0FX3X569X", Title: "T"})
	s := NewBookFileStore(store, "k")

	b, ok, err := s.Book(context.Background(), "B0FX3X569X")
	if err != nil {
		t.Fatalf("Book: %v", err)
	}
	if !ok || b.Title != "T" {
		t.Errorf("Book = %+v ok=%v", b, ok)
	}
	_, ok, _ = s.Book(context.Background(), "B0NOTFOUND01")
	if ok {
		t.Errorf("absent ASIN should return ok=false")
	}
}

func TestBookFileStore_Book_MissingObjectErrors(t *testing.T) {
	s := NewBookFileStore(NewMemStore(), "k")
	_, ok, err := s.Book(context.Background(), "B0FX3X569X")
	if !errors.Is(err, ErrObjectNotFound) {
		t.Fatalf("err = %v, want wrap of ErrObjectNotFound", err)
	}
	if ok {
		t.Errorf("ok = true, want false on missing object")
	}
}

func TestBookFileStore_Exists_MissingObjectErrors(t *testing.T) {
	s := NewBookFileStore(NewMemStore(), "k")
	exists, err := s.Exists(context.Background(), "B0FX3X569X")
	if !errors.Is(err, ErrObjectNotFound) {
		t.Fatalf("err = %v, want wrap of ErrObjectNotFound", err)
	}
	if exists {
		t.Errorf("exists = true, want false on missing object")
	}
}

func TestBookFileStore_RetriesOnConflict(t *testing.T) {
	store := &flakyStore{MemStore: NewMemStore(), conflicts: 2, conflictKey: "k"}
	store.Seed("k", `[]`)
	s := NewBookFileStore(store, "k")

	if err := s.Upsert(context.Background(), book.KindleBook{ASIN: "B0FX3X569X", Title: "T", ReleaseDate: futureRelease, CreatedAt: testNow}); err != nil {
		t.Fatalf("Upsert with conflict: %v", err)
	}
	obj, _ := store.Get(context.Background(), "k")
	records, _ := DecodeBooks(obj.Body)
	if findBookIndex(records, "B0FX3X569X") == -1 {
		t.Errorf("B0FX3X569X not present after retry: %+v", records)
	}
}

func TestAuthorFileStore_UpdateLatestRelease(t *testing.T) {
	store := NewMemStore()
	store.Seed("authors", `[{"Name":"海李","URL":"u","LatestReleaseDate":"2025-01-01T00:00:00Z","LatestReleaseTitle":"旧作","LatestReleaseURL":"old"}]`)
	s := NewAuthorFileStore(store, "authors")

	changed, err := s.UpdateLatestRelease(context.Background(), "海李", futureRelease, "新作", "new-url")
	if err != nil {
		t.Fatalf("UpdateLatestRelease: %v", err)
	}
	if !changed {
		t.Errorf("changed = false, want true")
	}
	obj, _ := store.Get(context.Background(), "authors")
	if !strings.Contains(string(obj.Body), "新作") {
		t.Errorf("LatestReleaseTitle not updated: %s", obj.Body)
	}
	if !strings.Contains(string(obj.Body), "new-url") {
		t.Errorf("LatestReleaseURL not updated: %s", obj.Body)
	}
}

func TestAuthorFileStore_NoChangeWhenOlder(t *testing.T) {
	store := NewMemStore()
	store.Seed("authors", `[{"Name":"海李","URL":"u","LatestReleaseDate":"2026-12-31T00:00:00Z","LatestReleaseTitle":"新作","LatestReleaseURL":"new"}]`)
	s := NewAuthorFileStore(store, "authors")

	changed, err := s.UpdateLatestRelease(context.Background(), "海李", pastRelease, "過去作", "old-url")
	if err != nil {
		t.Fatalf("UpdateLatestRelease: %v", err)
	}
	if changed {
		t.Errorf("changed = true, want false for older date")
	}
}

func TestAuthorFileStore_SortsAuthors(t *testing.T) {
	store := NewMemStore()
	store.Seed("authors", `[
        {"Name":"作者B","URL":"u","LatestReleaseDate":"2025-01-01T00:00:00Z","LatestReleaseTitle":"B作","LatestReleaseURL":"b"},
        {"Name":"作者A","URL":"u","LatestReleaseDate":"2026-12-31T00:00:00Z","LatestReleaseTitle":"A作","LatestReleaseURL":"a"}
    ]`)
	s := NewAuthorFileStore(store, "authors")

	_, err := s.UpdateLatestRelease(context.Background(), "作者A", futureRelease, "A作", "a")
	if err != nil {
		t.Fatalf("UpdateLatestRelease: %v", err)
	}
	obj, _ := store.Get(context.Background(), "authors")
	body := string(obj.Body)
	if strings.Index(body, "作者A") > strings.Index(body, "作者B") {
		t.Errorf("authors not sorted by release date desc:\n%s", body)
	}
}

func TestKnownStateQuerier(t *testing.T) {
	store := NewMemStore()
	seedBook(store, "notified", book.KindleBook{ASIN: "B0KINDLE01"})
	seedBook(store, "upcoming", book.KindleBook{ASIN: "B0KINDLE01"})
	seedBook(store, "paper", book.KindleBook{ASIN: "B0PAPER001"})
	store.Seed("unprocessed", `[]`)

	q := NewKnownStateQuerier(store, "notified", "upcoming", "unprocessed", "paper")
	state, err := q.KnownState(context.Background(), "B0KINDLE01", "B0PAPER001")
	if err != nil {
		t.Fatalf("KnownState: %v", err)
	}
	if !state.NotifiedExists {
		t.Errorf("NotifiedExists = false, want true")
	}
	if !state.UpcomingExists {
		t.Errorf("UpcomingExists = false, want true")
	}
	if state.UnprocessedExists {
		t.Errorf("UnprocessedExists = true, want false")
	}
	if !state.PaperBookExists {
		t.Errorf("PaperBookExists = false, want true")
	}
}

func TestKnownStateQuerier_MissingObjectErrors(t *testing.T) {
	store := NewMemStore()
	seedBook(store, "notified", book.KindleBook{ASIN: "B0KINDLE01"})
	seedBook(store, "upcoming", book.KindleBook{ASIN: "B0KINDLE01"})
	seedBook(store, "paper", book.KindleBook{ASIN: "B0PAPER001"})
	q := NewKnownStateQuerier(store, "notified", "upcoming", "unprocessed", "paper")
	if _, err := q.KnownState(context.Background(), "B0KINDLE01", "B0PAPER001"); !errors.Is(err, ErrObjectNotFound) {
		t.Fatalf("err = %v, want wrap of ErrObjectNotFound (object 欠落は error)", err)
	}
}

func TestBookFileStore_Delete_AbsentASINIsIdempotent(t *testing.T) {
	store := NewMemStore()
	seedBook(store, "paper", book.KindleBook{ASIN: "B0OTHER0001", Title: "keep"})
	s := NewBookFileStore(store, "paper")

	if err := s.Delete(context.Background(), "B0PAPER001"); err != nil {
		t.Fatalf("Delete on absent ASIN should not error: %v", err)
	}
	obj, _ := store.Get(context.Background(), "paper")
	records, _ := DecodeBooks(obj.Body)
	if len(records) != 1 || records[0].Book.ASIN != "B0OTHER0001" {
		t.Errorf("other record must be kept: %+v", records)
	}
}

func TestBookFileStore_Delete_MissingObjectErrors(t *testing.T) {
	store := NewMemStore()
	s := NewBookFileStore(store, "paper")

	if err := s.Delete(context.Background(), "B0PAPER001"); !errors.Is(err, ErrObjectNotFound) {
		t.Fatalf("err = %v, want wrap of ErrObjectNotFound", err)
	}
	if _, err := store.Get(context.Background(), "paper"); !errors.Is(err, ErrObjectNotFound) {
		t.Errorf("object must not be created on missing: %v", err)
	}
}

func TestBookFileStore_UpdateOneBook_SavesSortOrder(t *testing.T) {
	store := NewMemStore()
	seedBook(store, "k",
		book.KindleBook{ASIN: "B0OLD", Title: "old", ReleaseDate: pastRelease},
		book.KindleBook{ASIN: "B0NEW", Title: "new", ReleaseDate: futureRelease},
	)
	s := NewBookFileStore(store, "k")

	if _, err := s.UpdateOneBook(context.Background(), "B0OLD", func(b book.KindleBook) book.KindleBook {
		b.Title = "old!"
		return b
	}); err != nil {
		t.Fatalf("UpdateOneBook: %v", err)
	}
	obj, _ := store.Get(context.Background(), "k")
	records, _ := DecodeBooks(obj.Body)
	want := []string{"B0NEW", "B0OLD"}
	if !asinsEqual(asinOrder(records), want) {
		t.Errorf("order = %v, want %v\n%s", asinOrder(records), want, obj.Body)
	}
}

func TestBookFileStore_ApplyRetention_SavesSortOrder(t *testing.T) {
	store := NewMemStore()
	store.Seed("notified", `[
        {"ASIN":"B0FUTA","Title":"A","ReleaseDate":"2026-12-31T00:00:00Z","CurrentPrice":0,"MaxPrice":0,"URL":"","CreatedAt":"2026-01-01T00:00:00Z"},
        {"ASIN":"B0FUTB","Title":"B","ReleaseDate":"2027-06-01T00:00:00Z","CurrentPrice":0,"MaxPrice":0,"URL":"","CreatedAt":"2026-01-01T00:00:00Z"},
        {"ASIN":"B0PAST","Title":"P","ReleaseDate":"2020-01-01T00:00:00Z","CurrentPrice":0,"MaxPrice":0,"URL":"","CreatedAt":"2020-01-01T00:00:00Z"}
    ]`)
	s := NewBookFileStore(store, "notified")

	if _, err := s.ApplyRetentionAndExists(context.Background(), "B0FUTA", testNow); err != nil {
		t.Fatalf("ApplyRetentionAndExists: %v", err)
	}
	obj, _ := store.Get(context.Background(), "notified")
	records, _ := DecodeBooks(obj.Body)
	want := []string{"B0FUTB", "B0FUTA"}
	if !asinsEqual(asinOrder(records), want) {
		t.Errorf("order = %v, want %v\n%s", asinOrder(records), want, obj.Body)
	}
}

func TestBookFileStore_Delete_SavesSortOrder(t *testing.T) {
	store := NewMemStore()
	seedBook(store, "k",
		book.KindleBook{ASIN: "B0OLD", Title: "old", ReleaseDate: pastRelease},
		book.KindleBook{ASIN: "B0MID", Title: "mid", ReleaseDate: time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)},
		book.KindleBook{ASIN: "B0NEW", Title: "new", ReleaseDate: futureRelease},
	)
	s := NewBookFileStore(store, "k")

	if err := s.Delete(context.Background(), "B0MID"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	obj, _ := store.Get(context.Background(), "k")
	records, _ := DecodeBooks(obj.Body)
	want := []string{"B0NEW", "B0OLD"}
	if !asinsEqual(asinOrder(records), want) {
		t.Errorf("order = %v, want %v\n%s", asinOrder(records), want, obj.Body)
	}
}

func TestBookFileStore_Books_ReturnsAllInStoredOrder(t *testing.T) {
	store := NewMemStore()
	seedBook(store, "k",
		book.KindleBook{ASIN: "B0FUTURE001", Title: "未来", ReleaseDate: futureRelease, CurrentPrice: book.NewPrice(700)},
		book.KindleBook{ASIN: "B0PAST00001", Title: "過去", ReleaseDate: pastRelease, CurrentPrice: book.NewPrice(500)},
	)
	s := NewBookFileStore(store, "k")

	books, err := s.Books(context.Background())
	if err != nil {
		t.Fatalf("Books: %v", err)
	}
	got := []string{books[0].ASIN, books[1].ASIN}
	if !asinsEqual(got, []string{"B0FUTURE001", "B0PAST00001"}) {
		t.Errorf("order = %v, want [B0FUTURE001 B0PAST00001]", got)
	}
	if !books[0].CurrentPrice.Valid() || books[0].CurrentPrice.Yen() != 700 {
		t.Errorf("price not preserved: %+v", books[0].CurrentPrice)
	}
}

func TestBookFileStore_Books_MissingObjectErrors(t *testing.T) {
	s := NewBookFileStore(NewMemStore(), "k")
	books, err := s.Books(context.Background())
	if !errors.Is(err, ErrObjectNotFound) {
		t.Fatalf("err = %v, want wrap of ErrObjectNotFound", err)
	}
	if books != nil {
		t.Errorf("want nil books on missing object, got %+v", books)
	}
}

func TestAuthorFileStore_Authors_ReturnsAllInStoredOrder(t *testing.T) {
	store := NewMemStore()
	store.Seed("authors", `[{"Name":"作者A","URL":"u","LatestReleaseDate":"2026-12-31T00:00:00Z","LatestReleaseTitle":"A作","LatestReleaseURL":"a"},{"Name":"作者B","URL":"u","LatestReleaseDate":"2025-01-01T00:00:00Z","LatestReleaseTitle":"B作","LatestReleaseURL":"b"}]`)
	s := NewAuthorFileStore(store, "authors")

	authors, err := s.Authors(context.Background())
	if err != nil {
		t.Fatalf("Authors: %v", err)
	}
	if len(authors) != 2 {
		t.Fatalf("len = %d, want 2", len(authors))
	}
	if authors[0].Name != "作者A" || authors[1].Name != "作者B" {
		t.Errorf("order = %v, want [作者A 作者B]", []string{authors[0].Name, authors[1].Name})
	}
}

func TestAuthorFileStore_Authors_MissingObjectErrors(t *testing.T) {
	s := NewAuthorFileStore(NewMemStore(), "authors")
	authors, err := s.Authors(context.Background())
	if !errors.Is(err, ErrObjectNotFound) {
		t.Fatalf("err = %v, want wrap of ErrObjectNotFound", err)
	}
	if authors != nil {
		t.Errorf("want nil authors on missing object, got %+v", authors)
	}
}

func findAuthorLatestRelease(t *testing.T, s *AuthorFileStore, name string) time.Time {
	t.Helper()
	authors, err := s.Authors(context.Background())
	if err != nil {
		t.Fatalf("Authors: %v", err)
	}
	for _, a := range authors {
		if a.Name == name {
			return a.LatestReleaseDate
		}
	}
	t.Fatalf("author %q not found", name)
	return time.Time{}
}

func TestAuthorFileStore_UpdateLatestRelease_KeepsMaxDateRegardlessOfOrder(t *testing.T) {
	const seedJSON = `[{"Name":"海李","URL":"u","LatestReleaseDate":"2025-01-01T00:00:00Z","LatestReleaseTitle":"旧作","LatestReleaseURL":"old"}]`
	older := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	newer := time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC)

	orders := []struct {
		name  string
		dates []time.Time
	}{
		{name: "古い順(older→newer)で呼んでもnewerが残る", dates: []time.Time{older, newer}},
		{name: "新しい順(newer→older)で呼んでもnewerが残る", dates: []time.Time{newer, older}},
	}
	for _, tc := range orders {
		t.Run(tc.name, func(t *testing.T) {
			store := NewMemStore()
			store.Seed("authors", seedJSON)
			s := NewAuthorFileStore(store, "authors")

			for _, d := range tc.dates {
				if _, err := s.UpdateLatestRelease(context.Background(), "海李", d, "作", "u"); err != nil {
					t.Fatalf("UpdateLatestRelease(%v): %v", d, err)
				}
			}
			got := findAuthorLatestRelease(t, s, "海李")
			if !got.Equal(newer) {
				t.Errorf("LatestReleaseDate = %v, want %v (処理順に依存せず最大日付)", got, newer)
			}
		})
	}
}

func TestAuthorFileStore_UpdateLatestRelease_AbsentAuthorNoReadd(t *testing.T) {
	store := NewMemStore()
	store.Seed("authors", `[{"Name":"別人","URL":"u","LatestReleaseDate":"2025-01-01T00:00:00Z","LatestReleaseTitle":"x","LatestReleaseURL":"y"}]`)
	s := NewAuthorFileStore(store, "authors")

	changed, err := s.UpdateLatestRelease(context.Background(), "不在作者", futureRelease, "新作", "u")
	if err != nil {
		t.Fatalf("UpdateLatestRelease: %v", err)
	}
	if changed {
		t.Errorf("changed = true, want false for absent author")
	}
	authors, _ := s.Authors(context.Background())
	if len(authors) != 1 {
		t.Errorf("absent author must not be re-added: got %+v", authors)
	}
	for _, a := range authors {
		if a.Name == "不在作者" {
			t.Errorf("absent author was re-added: %+v", authors)
		}
	}
}

func TestBookFileStore_ApplyRetentionAndExists_Boundary(t *testing.T) {
	store := NewMemStore()
	store.Seed("notified", `[
        {"ASIN":"B0EQULA0001","Title":"同時刻","ReleaseDate":"2026-08-09T00:00:00Z","CurrentPrice":0,"MaxPrice":0,"URL":"","CreatedAt":"2026-01-01T00:00:00Z"},
        {"ASIN":"B0NEXT00001","Title":"翌日","ReleaseDate":"2026-08-10T00:00:00Z","CurrentPrice":0,"MaxPrice":0,"URL":"","CreatedAt":"2026-01-01T00:00:00Z"}
    ]`)
	s := NewBookFileStore(store, "notified")

	if _, err := s.ApplyRetentionAndExists(context.Background(), "B0EQULA0001", testNow); err != nil {
		t.Fatalf("ApplyRetentionAndExists: %v", err)
	}
	obj, _ := store.Get(context.Background(), "notified")
	body := string(obj.Body)
	if strings.Contains(body, "B0EQULA0001") {
		t.Errorf("発売日==now は保存期間適用で除外される: %s", body)
	}
	if !strings.Contains(body, "B0NEXT00001") {
		t.Errorf("発売日が now より1日後は保持される: %s", body)
	}
}
