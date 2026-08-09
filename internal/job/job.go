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

// Kind はジョブ種別。
type Kind string

const (
	KindSaleCheck           Kind = "sale_check"
	KindSaleFinalize        Kind = "sale_finalize"
	KindNewReleaseSearch    Kind = "new_release_search"
	KindNewReleaseResult    Kind = "new_release_result"
	KindNewReleaseDetail    Kind = "new_release_detail"
	KindPaperToKindleCheck  Kind = "paper_to_kindle_check"
	KindPaperToKindleDetail Kind = "paper_to_kindle_detail"
	KindGistUpdate          Kind = "gist_update"
)

// CheckType はチェック種別。
type CheckType string

const (
	CheckSale          CheckType = "sale"
	CheckNewRelease    CheckType = "new_release"
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

// Target はジョブ種別ごとの対象情報。不要な field は空値で送らない（omitempty）。
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
	ErrUnsupportedVersion = errors.New("unsupported job version")
	ErrUnknownKind        = errors.New("unknown job kind")
	ErrMissingField       = errors.New("missing required target field")
	ErrInvalidASIN        = errors.New("invalid ASIN format")
)

var (
	// kindleAsinRe は10文字の英数字（Kindle ASIN）。
	kindleAsinRe = regexp.MustCompile(`^[A-Z0-9]{10}$`)
	// isbnRe は10〜13桁の数字のみ（紙書籍 ISBN）。paper_books_asins 由来の対象を弾かないため許容する。
	isbnRe = regexp.MustCompile(`^\d{10,13}$`)
)

// Validate は schema 検証を行う。未対応 version、未知 kind、必須 field 欠落、ASIN 形式不正を検出する。
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
	case KindNewReleaseDetail:
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

// requireASIN は ASIN が必須かつ形式（10文字英数字 または 10〜13桁数字）を満たすかを検証する。
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
	case KindSaleCheck, KindNewReleaseSearch, KindNewReleaseDetail, KindPaperToKindleCheck, KindPaperToKindleDetail:
		return 1
	default:
		return 0
	}
}

// Decode は JSON を Job へ復号し schema 検証する。
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

// Encode は Job を JSON へ符号化する。送信側で事前に Validate 済みであることを前提とする。
func (j Job) Encode() ([]byte, error) {
	return json.Marshal(j)
}
