package job

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func validBaseJob(kind Kind, target Target) Job {
	return Job{
		Version:     Version,
		JobID:       "test-job",
		Kind:        kind,
		CheckType:   CheckSale,
		CycleID:     "sale:2026-07-23T00:00:00Z",
		ScheduledAt: time.Date(2026, 7, 23, 0, 0, 0, 0, time.UTC),
		Target:      target,
	}
}

func TestValidate(t *testing.T) {
	product := &SearchProduct{ASIN: "B0FX3X569X", Title: "T", KindlePrice: 759, ItemType: ItemTypeKindle}

	tests := []struct {
		name    string
		job     Job
		wantErr error
	}{
		{name: "sale_checkはASIN必須", job: validBaseJob(KindSaleCheck, Target{ASIN: "B0FX3X569X"}), wantErr: nil},
		{name: "sale_checkのASIN空はMissingField", job: validBaseJob(KindSaleCheck, Target{}), wantErr: ErrMissingField},
		{name: "sale_checkのASIN形式不正はInvalidASIN", job: validBaseJob(KindSaleCheck, Target{ASIN: "short"}), wantErr: ErrInvalidASIN},
		{name: "ISBN-13は紙書籍ASINとして許容する", job: validBaseJob(KindPaperToKindleCheck, Target{ASIN: "1234567890123"}), wantErr: nil},
		{name: "sale_finalizeはtarget不要", job: validBaseJob(KindSaleFinalize, Target{}), wantErr: nil},
		{name: "new_release_searchはauthor_name必須", job: validBaseJob(KindNewReleaseSearch, Target{AuthorName: "海李"}), wantErr: nil},
		{name: "new_release_searchのauthor_name空はMissingField", job: validBaseJob(KindNewReleaseSearch, Target{}), wantErr: ErrMissingField},
		{name: "new_release_resultはasinとauthorとproduct必須", job: validBaseJob(KindNewReleaseResult, Target{ASIN: "B0FX3X569X", AuthorName: "海李", Product: product}), wantErr: nil},
		{name: "new_release_resultのproduct nilはMissingField", job: validBaseJob(KindNewReleaseResult, Target{ASIN: "B0FX3X569X", AuthorName: "海李"}), wantErr: ErrMissingField},
		{name: "new_release_resultのitem_type空はInvalidItemType", job: validBaseJob(KindNewReleaseResult, Target{ASIN: "B0FX3X569X", AuthorName: "海李", Product: &SearchProduct{ASIN: "B0FX3X569X", ItemType: ""}}), wantErr: ErrInvalidItemType},
		{name: "new_release_resultのitem_type未知値はInvalidItemType", job: validBaseJob(KindNewReleaseResult, Target{ASIN: "B0FX3X569X", AuthorName: "海李", Product: &SearchProduct{ASIN: "B0FX3X569X", ItemType: "ebooks"}}), wantErr: ErrInvalidItemType},
		{name: "new_release_detailはasinとauthor必須", job: validBaseJob(KindNewReleaseDetail, Target{ASIN: "B0FX3X569X", AuthorName: "海李"}), wantErr: nil},
		{name: "paper_to_kindle_checkはASIN必須", job: validBaseJob(KindPaperToKindleCheck, Target{ASIN: "B0FX3X569X"}), wantErr: nil},
		{name: "paper_to_kindle_detailはasinとsource_asin必須", job: validBaseJob(KindPaperToKindleDetail, Target{ASIN: "B0FX3X569X", SourceASIN: "B0PAPER001"}), wantErr: nil},
		{name: "paper_to_kindle_detailのsource_asin空はMissingField", job: validBaseJob(KindPaperToKindleDetail, Target{ASIN: "B0FX3X569X"}), wantErr: ErrMissingField},
		{name: "paper_to_kindle_detailのsource_asin形式不正はInvalidASIN", job: validBaseJob(KindPaperToKindleDetail, Target{ASIN: "B0FX3X569X", SourceASIN: "short"}), wantErr: ErrInvalidASIN},
		{name: "paper_to_kindle_detailのISBN-13をsource_asinとして許容する", job: validBaseJob(KindPaperToKindleDetail, Target{ASIN: "B0FX3X569X", SourceASIN: "1234567890123"}), wantErr: nil},
		{name: "gist_updateはgist_type必須", job: validBaseJob(KindGistUpdate, Target{GistType: "sale"}), wantErr: nil},
		{name: "gist_updateのgist_type空はMissingField", job: validBaseJob(KindGistUpdate, Target{}), wantErr: ErrMissingField},
		{name: "sale_checkのISBN-10をASINとして許容する", job: validBaseJob(KindSaleCheck, Target{ASIN: "1234567890"}), wantErr: nil},
		{name: "sale_checkの小文字ASINはInvalidASIN", job: validBaseJob(KindSaleCheck, Target{ASIN: "b0fx3x569x"}), wantErr: ErrInvalidASIN},
		{name: "new_release_resultのASIN空はMissingField", job: validBaseJob(KindNewReleaseResult, Target{AuthorName: "海李", Product: product}), wantErr: ErrMissingField},
		{name: "new_release_resultのASIN形式不正はInvalidASIN", job: validBaseJob(KindNewReleaseResult, Target{ASIN: "short", AuthorName: "海李", Product: product}), wantErr: ErrInvalidASIN},
		{name: "new_release_resultのauthor_name空はMissingField", job: validBaseJob(KindNewReleaseResult, Target{ASIN: "B0FX3X569X", Product: product}), wantErr: ErrMissingField},
		{name: "new_release_detailのASIN空はMissingField", job: validBaseJob(KindNewReleaseDetail, Target{AuthorName: "海李"}), wantErr: ErrMissingField},
		{name: "new_release_detailのASIN形式不正はInvalidASIN", job: validBaseJob(KindNewReleaseDetail, Target{ASIN: "short", AuthorName: "海李"}), wantErr: ErrInvalidASIN},
		{name: "new_release_detailのauthor_name空はMissingField", job: validBaseJob(KindNewReleaseDetail, Target{ASIN: "B0FX3X569X"}), wantErr: ErrMissingField},
		{name: "paper_to_kindle_checkのASIN空はMissingField", job: validBaseJob(KindPaperToKindleCheck, Target{}), wantErr: ErrMissingField},
		{name: "paper_to_kindle_checkのASIN形式不正はInvalidASIN", job: validBaseJob(KindPaperToKindleCheck, Target{ASIN: "short"}), wantErr: ErrInvalidASIN},
		{name: "paper_to_kindle_detailのASIN空はMissingField", job: validBaseJob(KindPaperToKindleDetail, Target{SourceASIN: "B0PAPER001"}), wantErr: ErrMissingField},
		{name: "paper_to_kindle_detailのASIN形式不正はInvalidASIN", job: validBaseJob(KindPaperToKindleDetail, Target{ASIN: "short", SourceASIN: "B0PAPER001"}), wantErr: ErrInvalidASIN},
		{name: "未対応versionはUnsupportedVersion", job: func() Job { j := validBaseJob(KindSaleCheck, Target{ASIN: "B0FX3X569X"}); j.Version = 2; return j }(), wantErr: ErrUnsupportedVersion},
		{name: "version欠落(0)はUnsupportedVersion", job: func() Job { j := validBaseJob(KindSaleCheck, Target{ASIN: "B0FX3X569X"}); j.Version = 0; return j }(), wantErr: ErrUnsupportedVersion},
		{name: "未知kindはUnknownKind", job: validBaseJob("unknown_kind", Target{}), wantErr: ErrUnknownKind},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.job.Validate()
			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("Validate unexpected err = %v", err)
				}
				return
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("Validate err = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

func TestMessageGroup(t *testing.T) {
	tests := []struct {
		kind Kind
		want string
	}{
		{KindGistUpdate, "external-updates"},
		{KindSaleCheck, "amazon-requests"},
		{KindSaleFinalize, "amazon-requests"},
		{KindNewReleaseSearch, "amazon-requests"},
		{KindNewReleaseResult, "amazon-requests"},
		{KindNewReleaseDetail, "amazon-requests"},
		{KindPaperToKindleCheck, "amazon-requests"},
		{KindPaperToKindleDetail, "amazon-requests"},
	}
	for _, tc := range tests {
		if got := MessageGroup(tc.kind); got != tc.want {
			t.Errorf("MessageGroup(%v) = %q, want %q", tc.kind, got, tc.want)
		}
	}
}

func TestAmazonRequests(t *testing.T) {
	tests := []struct {
		kind Kind
		want int
	}{
		{KindSaleCheck, 1},
		{KindNewReleaseSearch, 1},
		{KindNewReleaseDetail, 1},
		{KindPaperToKindleCheck, 1},
		{KindPaperToKindleDetail, 1},
		{KindSaleFinalize, 0},
		{KindNewReleaseResult, 0},
		{KindGistUpdate, 0},
	}
	for _, tc := range tests {
		if got := AmazonRequests(tc.kind); got != tc.want {
			t.Errorf("AmazonRequests(%v) = %d, want %d", tc.kind, got, tc.want)
		}
	}
}

func TestDecode(t *testing.T) {
	saleCheckJSON := `{"version":1,"job_id":"j1","kind":"sale_check","check_type":"sale","cycle_id":"sale:2026-07-23T00:00:00Z","scheduled_at":"2026-07-23T00:00:00Z","target":{"asin":"B0FX3X569X"}}`

	tests := []struct {
		name    string
		data    string
		wantErr error
	}{
		{name: "正常なsale_checkを復号する", data: saleCheckJSON, wantErr: nil},
		{name: "不正JSONはdecodeエラー", data: `{not json`, wantErr: nil},
		{name: "未対応versionはUnsupportedVersion", data: `{"version":2,"kind":"sale_check"}`, wantErr: ErrUnsupportedVersion},
		{name: "未知kindはUnknownKind", data: `{"version":1,"kind":"unknown"}`, wantErr: ErrUnknownKind},
		{name: "必須field欠落はMissingField", data: `{"version":1,"kind":"sale_check","target":{}}`, wantErr: ErrMissingField},
		{name: "version欠落はUnsupportedVersion", data: `{"kind":"sale_check","target":{"asin":"B0FX3X569X"}}`, wantErr: ErrUnsupportedVersion},
		{name: "ASIN形式不正はInvalidASIN", data: `{"version":1,"kind":"sale_check","target":{"asin":"short"}}`, wantErr: ErrInvalidASIN},
		{name: "new_release_resultのitem_type空はInvalidItemType", data: `{"version":1,"kind":"new_release_result","target":{"asin":"B0FX3X569X","author_name":"海李","product":{"asin":"B0FX3X569X","item_type":""}}}`, wantErr: ErrInvalidItemType},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Decode([]byte(tc.data))
			switch {
			case tc.name == "不正JSONはdecodeエラー":
				if err == nil {
					t.Fatalf("Decode expected error for invalid JSON")
				}
			case tc.wantErr == nil:
				if err != nil {
					t.Fatalf("Decode unexpected err = %v", err)
				}
			default:
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("Decode err = %v, want %v", err, tc.wantErr)
				}
			}
		})
	}
}

func TestEncodeOmitsUnusedTargetFields(t *testing.T) {
	tests := []struct {
		name   string
		job    Job
		keys   []string
		extras []string
	}{
		{
			name:   "sale_checkはtargetにasinだけを出力する",
			job:    validBaseJob(KindSaleCheck, Target{ASIN: "B0FX3X569X"}),
			keys:   []string{"asin"},
			extras: []string{"source_asin", "author_name", "product", "gist_type"},
		},
		{
			name:   "sale_finalizeはtargetにfieldを出力しない",
			job:    validBaseJob(KindSaleFinalize, Target{}),
			keys:   nil,
			extras: []string{"asin", "source_asin", "author_name", "product", "gist_type"},
		},
		{
			name:   "new_release_searchはtargetにauthor_nameだけを出力する",
			job:    validBaseJob(KindNewReleaseSearch, Target{AuthorName: "海李"}),
			keys:   []string{"author_name"},
			extras: []string{"asin", "source_asin", "product", "gist_type"},
		},
		{
			name:   "new_release_resultはtargetにasinとauthor_nameとproductを出力する",
			job:    validBaseJob(KindNewReleaseResult, Target{ASIN: "B0FX3X569X", AuthorName: "海李", Product: &SearchProduct{ASIN: "B0FX3X569X", ItemType: ItemTypeKindle}}),
			keys:   []string{"asin", "author_name", "product"},
			extras: []string{"source_asin", "gist_type"},
		},
		{
			name:   "new_release_detailはtargetにasinとauthor_nameを出力する",
			job:    validBaseJob(KindNewReleaseDetail, Target{ASIN: "B0FX3X569X", AuthorName: "海李"}),
			keys:   []string{"asin", "author_name"},
			extras: []string{"source_asin", "product", "gist_type"},
		},
		{
			name:   "paper_to_kindle_checkはtargetにasinだけを出力する",
			job:    validBaseJob(KindPaperToKindleCheck, Target{ASIN: "B0FX3X569X"}),
			keys:   []string{"asin"},
			extras: []string{"source_asin", "author_name", "product", "gist_type"},
		},
		{
			name:   "paper_to_kindle_detailはtargetにasinとsource_asinを出力する",
			job:    validBaseJob(KindPaperToKindleDetail, Target{ASIN: "B0FX3X569X", SourceASIN: "B0PAPER001"}),
			keys:   []string{"asin", "source_asin"},
			extras: []string{"author_name", "product", "gist_type"},
		},
		{
			name:   "gist_updateはtargetにgist_typeだけを出力する",
			job:    validBaseJob(KindGistUpdate, Target{GistType: "sale"}),
			keys:   []string{"gist_type"},
			extras: []string{"asin", "source_asin", "author_name", "product"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			data, err := tc.job.Encode()
			if err != nil {
				t.Fatalf("Encode unexpected err = %v", err)
			}
			var raw map[string]any
			if err := json.Unmarshal(data, &raw); err != nil {
				t.Fatalf("re-unmarshal failed: %v", err)
			}
			target, ok := raw["target"].(map[string]any)
			if !ok {
				t.Fatalf("target is not an object: %v", raw["target"])
			}
			for _, k := range tc.keys {
				if _, ok := target[k]; !ok {
					t.Errorf("target missing expected key %q in %v", k, target)
				}
			}
			for _, k := range tc.extras {
				if _, ok := target[k]; ok {
					t.Errorf("target should not contain unused key %q in %v", k, target)
				}
			}
		})
	}
}

func TestEncodeDecodeResultItemTypeRoundTrip(t *testing.T) {
	src := validBaseJob(KindNewReleaseResult, Target{
		ASIN: "B0FX3X569X", AuthorName: "海李",
		Product: &SearchProduct{ASIN: "B0FX3X569X", Title: "T", ItemType: ItemTypeKindle},
	})
	data, err := src.Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	got, err := Decode(data)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if got.Target.Product == nil || got.Target.Product.ItemType != ItemTypeKindle {
		t.Fatalf("item_type round-trip = %q, want %q", getItemType(got.Target.Product), ItemTypeKindle)
	}
	if err := got.Validate(); err != nil {
		t.Fatalf("Validate after decode: %v", err)
	}
	if !strings.Contains(string(data), `"item_type":"kindle"`) {
		t.Fatalf("encoded JSON lacks canonical item_type: %s", data)
	}
}

func getItemType(p *SearchProduct) string {
	if p == nil {
		return "<nil>"
	}
	return p.ItemType
}

func TestEncodeScheduledAtAsRFC3339(t *testing.T) {
	j := validBaseJob(KindSaleCheck, Target{ASIN: "B0FX3X569X"})
	data, err := j.Encode()
	if err != nil {
		t.Fatalf("Encode unexpected err = %v", err)
	}
	if !strings.Contains(string(data), `"scheduled_at":"2026-07-23T00:00:00Z"`) {
		t.Fatalf("Encode scheduled_at not RFC3339 UTC: %s", data)
	}
}

func TestEncodeDecodeRoundTrip_AllKinds(t *testing.T) {
	product := &SearchProduct{
		ASIN: "B0FX3X569X", Title: "T", URL: "https://example.jp/u",
		KindlePrice: 759, ReleaseDate: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC),
		AuthorLabel: "海李", ItemType: ItemTypeKindle,
	}
	cases := []struct {
		name string
		job  Job
	}{
		{"sale_check", validBaseJob(KindSaleCheck, Target{ASIN: "B0FX3X569X"})},
		{"sale_finalize", validBaseJob(KindSaleFinalize, Target{})},
		{"new_release_search", validBaseJob(KindNewReleaseSearch, Target{AuthorName: "海李"})},
		{"new_release_result", validBaseJob(KindNewReleaseResult, Target{ASIN: "B0FX3X569X", AuthorName: "海李", Product: product})},
		{"new_release_detail", validBaseJob(KindNewReleaseDetail, Target{ASIN: "B0FX3X569X", AuthorName: "海李"})},
		{"paper_to_kindle_check", validBaseJob(KindPaperToKindleCheck, Target{ASIN: "B0FX3X569X"})},
		{"paper_to_kindle_detail", validBaseJob(KindPaperToKindleDetail, Target{ASIN: "B0FX3X569X", SourceASIN: "B0PAPER001"})},
		{"gist_update", validBaseJob(KindGistUpdate, Target{GistType: "sale"})},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			data, err := tc.job.Encode()
			if err != nil {
				t.Fatalf("Encode: %v", err)
			}
			got, err := Decode(data)
			if err != nil {
				t.Fatalf("Decode: %v", err)
			}
			if err := got.Validate(); err != nil {
				t.Fatalf("Validate after decode: %v", err)
			}
			if got.Version != tc.job.Version {
				t.Errorf("Version = %d, want %d", got.Version, tc.job.Version)
			}
			if got.Kind != tc.job.Kind {
				t.Errorf("Kind = %q, want %q", got.Kind, tc.job.Kind)
			}
			if got.JobID != tc.job.JobID {
				t.Errorf("JobID = %q, want %q", got.JobID, tc.job.JobID)
			}
			if got.CycleID != tc.job.CycleID {
				t.Errorf("CycleID = %q, want %q", got.CycleID, tc.job.CycleID)
			}
			if got.CheckType != tc.job.CheckType {
				t.Errorf("CheckType = %q, want %q", got.CheckType, tc.job.CheckType)
			}
			if !got.ScheduledAt.Equal(tc.job.ScheduledAt) {
				t.Errorf("ScheduledAt = %v, want %v", got.ScheduledAt, tc.job.ScheduledAt)
			}
			if !reflect.DeepEqual(got.Target, tc.job.Target) {
				t.Errorf("Target = %+v, want %+v", got.Target, tc.job.Target)
			}
		})
	}
}
