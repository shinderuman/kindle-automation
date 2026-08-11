// Package job は SQS へ投入するジョブメッセージの型、検証、routing を提供する。
// 業務判定やHTMLセレクタは持たず、メッセージの decode/encode と schema 検証だけを担う。
package job

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"
)

// Version はジョブメッセージ schema の現行版（SPECIFICATION.md 7.2）。
const Version = 1

// ItemTypeKindle は new_release_result の product.item_type の正規値（SPECIFICATION.md 7.2, 13.4）。
// 検索結果でKindle版と確定した候補だけがこの値を持つ。
const ItemTypeKindle = "kindle"

// Kind はジョブメッセージの種別（SPECIFICATION.md 7.3）。
type Kind string

const (
	// KindSaleCheck は商品ページでセール条件を判定するジョブ（SPECIFICATION.md 7.3）。
	KindSaleCheck Kind = "sale_check"
	// KindSaleFinalize はセール周回の終了で Sale 用 gist_update を投入するジョブ（SPECIFICATION.md 7.3）。
	KindSaleFinalize Kind = "sale_finalize"
	// KindNewReleaseSearch は検索ページから作者の候補 ASIN を抽出するジョブ（SPECIFICATION.md 7.3）。
	KindNewReleaseSearch Kind = "new_release_search"
	// KindNewReleaseResult は必須項目が揃った検索候補の判定・保存を行うジョブ（SPECIFICATION.md 7.3）。
	KindNewReleaseResult Kind = "new_release_result"
	// KindNewReleaseDetail は候補の商品ページで発売日・価格・Kindle版確認を行うジョブ（SPECIFICATION.md 7.3）。
	KindNewReleaseDetail Kind = "new_release_detail"
	// KindNewReleasePaperDetail はISBN候補の紙書籍ページを取得し paper_books_asins.json へ冪等upsertするジョブ（SPECIFICATION.md 7.3, 13.4）。
	KindNewReleasePaperDetail Kind = "new_release_paper_detail"
	// KindPaperToKindleCheck は紙書籍ページで Kindle 版スウォッチを確認するジョブ（SPECIFICATION.md 7.3）。
	KindPaperToKindleCheck Kind = "paper_to_kindle_check"
	// KindPaperToKindleDetail は Kindle 版候補の商品ページで検証・保存を行うジョブ（SPECIFICATION.md 7.3）。
	KindPaperToKindleDetail Kind = "paper_to_kindle_detail"
	// KindGistUpdate は Sale・Author・Paper-to-Kindle Gist を再生成するジョブ（SPECIFICATION.md 7.3/15）。
	KindGistUpdate Kind = "gist_update"
)

// CheckType は schedule-checks 起動ごとのチェック種別（SPECIFICATION.md 5.1/6）。
type CheckType string

const (
	// CheckSale はセール周期の check_type 値。
	CheckSale CheckType = "sale"
	// CheckNewRelease は新刊周期の check_type 値。
	CheckNewRelease CheckType = "new_release"
	// CheckPaperToKindle は紙書籍・Kindle版周期の check_type 値。
	CheckPaperToKindle CheckType = "paper_to_kindle"
)

// SearchProduct は検索HTMLから抽出した候補商品（SPECIFICATION.md 7.2）。
// 管理値（MaxPrice, CreatedAt, 通知状態）は持たない。
type SearchProduct struct {
	ASIN        string    `json:"asin"`
	Title       string    `json:"title"`
	URL         string    `json:"url"`
	KindlePrice float64   `json:"kindle_price"`
	ReleaseDate time.Time `json:"release_date"`
	AuthorLabel string    `json:"author_label"`
	ItemType    string    `json:"item_type"`
}

// Target の不要な field は空値で送らない（omitempty）。
type Target struct {
	ASIN       string         `json:"asin,omitempty"`
	SourceASIN string         `json:"source_asin,omitempty"`
	AuthorName string         `json:"author_name,omitempty"`
	Product    *SearchProduct `json:"product,omitempty"`
	GistType   string         `json:"gist_type,omitempty"`
}

// Job は SQS メッセージの本体（SPECIFICATION.md 7.2）。
type Job struct {
	Version     int       `json:"version"`
	JobID       string    `json:"job_id"`
	Kind        Kind      `json:"kind"`
	CheckType   CheckType `json:"check_type"`
	CycleID     string    `json:"cycle_id"`
	ScheduledAt time.Time `json:"scheduled_at"`
	Target      Target    `json:"target"`
}

// schema 検証エラー。いずれもAmazonへアクセスせずDLQへ残せる terminal 扱い（SPECIFICATION.md 7.2）。
var (
	// ErrUnsupportedVersion は job メッセージの version が現行 schema と一致しない。
	ErrUnsupportedVersion = errors.New("unsupported job version")
	// ErrUnknownKind は Job.Kind が未知の値。
	ErrUnknownKind = errors.New("unknown job kind")
	// ErrMissingField は kind ごとの必須 target field が空。
	ErrMissingField = errors.New("missing required target field")
	// ErrInvalidASIN は ASIN/ISBN の形式が不正。
	ErrInvalidASIN = errors.New("invalid ASIN format")
	// ErrInvalidItemType は product.item_type が正規値 "kindle" でない（SPECIFICATION.md 7.2/13.4）。
	ErrInvalidItemType = errors.New("invalid product item_type")
)

var (
	kindleAsinRe = regexp.MustCompile(`^[A-Z0-9]{10}$`)
	// paper_books_asins 由来の対象を弾かないため ISBN（10〜13桁の数字）を許容する。
	isbnRe = regexp.MustCompile(`^\d{10,13}$`)
)

// Validate は Job の schema と kind ごとの必須 target field を検証する（SPECIFICATION.md 7.2）。
func (j Job) Validate() error {
	if j.Version != Version {
		return ErrUnsupportedVersion
	}
	switch j.Kind {
	case KindSaleCheck:
		if err := requireASIN(j.Target.ASIN); err != nil {
			return err
		}
	case KindSaleFinalize:
		// target field なし。
	case KindNewReleaseSearch:
		if j.Target.AuthorName == "" {
			return ErrMissingField
		}
	case KindNewReleaseResult:
		if err := requireASIN(j.Target.ASIN); err != nil {
			return err
		}
		if j.Target.AuthorName == "" || j.Target.Product == nil {
			return ErrMissingField
		}
		// product.item_type はKindle版確定候補だけが持つ正規値（SPECIFICATION.md 7.2, 13.4）。
		// 空や未知値は検索種別確認の省略/誤分類を意味するため受け付けない。
		if j.Target.Product.ItemType != ItemTypeKindle {
			return ErrInvalidItemType
		}
	case KindNewReleaseDetail:
		if err := requireASIN(j.Target.ASIN); err != nil {
			return err
		}
		if j.Target.AuthorName == "" {
			return ErrMissingField
		}
	case KindNewReleasePaperDetail:
		if err := requireASIN(j.Target.ASIN); err != nil {
			return err
		}
		if j.Target.AuthorName == "" {
			return ErrMissingField
		}
	case KindPaperToKindleCheck:
		if err := requireASIN(j.Target.ASIN); err != nil {
			return err
		}
	case KindPaperToKindleDetail:
		if err := requireASIN(j.Target.ASIN); err != nil {
			return err
		}
		if err := requireASIN(j.Target.SourceASIN); err != nil {
			return err
		}
	case KindGistUpdate:
		if j.Target.GistType == "" {
			return ErrMissingField
		}
	default:
		return ErrUnknownKind
	}
	return nil
}

func requireASIN(asin string) error {
	if asin == "" {
		return ErrMissingField
	}
	if !kindleAsinRe.MatchString(asin) && !isbnRe.MatchString(asin) {
		return ErrInvalidASIN
	}
	return nil
}

// MessageGroup はジョブ種別に応じた SQS MessageGroupId を返す（SPECIFICATION.md 7.1/7.3）。
// gist_update だけは external-updates。Amazonへアクセスし得るジョブは amazon-requests。
func MessageGroup(k Kind) string {
	if k == KindGistUpdate {
		return "external-updates"
	}
	return "amazon-requests"
}

// AmazonRequests は1起動でAmazonへアクセスする回数（0 または 1）を返す（SPECIFICATION.md 7.3）。
func AmazonRequests(k Kind) int {
	switch k {
	case KindSaleCheck, KindNewReleaseSearch, KindNewReleaseDetail, KindNewReleasePaperDetail, KindPaperToKindleCheck, KindPaperToKindleDetail:
		return 1
	default:
		return 0
	}
}

// Decode は JSON を Job へ復元し schema 検証を経て返す（SPECIFICATION.md 7.2）。
func Decode(data []byte) (Job, error) {
	var j Job
	if err := json.Unmarshal(data, &j); err != nil {
		return Job{}, fmt.Errorf("job decode: %w", err)
	}
	if err := j.Validate(); err != nil {
		return Job{}, err
	}
	return j, nil
}

// Encode は送信側で事前に Validate 済みであることを前提とする。
func (j Job) Encode() ([]byte, error) {
	return json.Marshal(j)
}
