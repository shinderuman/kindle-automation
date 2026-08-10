package config

import (
	"context"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	ssmtypes "github.com/aws/aws-sdk-go-v2/service/ssm/types"
	"github.com/aws/smithy-go"
)

type stubGetter struct {
	values map[string]string
	errs   map[string]error
}

func (s *stubGetter) GetParameter(_ context.Context, in *ssm.GetParameterInput, _ ...func(*ssm.Options)) (*ssm.GetParameterOutput, error) {
	name := *in.Name
	if err, ok := s.errs[name]; ok {
		return nil, err
	}
	if v, ok := s.values[name]; ok {
		return &ssm.GetParameterOutput{Parameter: &ssmtypes.Parameter{Value: aws.String(v)}}, nil
	}
	return nil, &smithy.GenericAPIError{Code: "ParameterNotFound"}
}

func TestLoadOneSecret_PrefersSecure(t *testing.T) {
	g := &stubGetter{values: map[string]string{
		secureSSMPath + "/" + KeySlackBotToken: "secure-token",
		plainSSMPath + "/" + KeySlackBotToken:  "plain-token",
	}}
	v, err := loadOneSecret(context.Background(), g, KeySlackBotToken)
	if err != nil {
		t.Fatalf("loadOneSecret: %v", err)
	}
	if v != "secure-token" {
		t.Errorf("got %q, want secure-token", v)
	}
}

func TestLoadOneSecret_FallsBackToPlain(t *testing.T) {
	g := &stubGetter{values: map[string]string{
		plainSSMPath + "/" + KeySlackBotToken: "plain-token",
	}}
	v, err := loadOneSecret(context.Background(), g, KeySlackBotToken)
	if err != nil {
		t.Fatalf("loadOneSecret: %v", err)
	}
	if v != "plain-token" {
		t.Errorf("got %q, want plain-token", v)
	}
}

func TestLoadOneSecret_NotFoundInBoth(t *testing.T) {
	g := &stubGetter{}
	_, err := loadOneSecret(context.Background(), g, KeySlackBotToken)
	if !errors.Is(err, ErrSecretNotFound) {
		t.Fatalf("want ErrSecretNotFound, got %v", err)
	}
}

func TestLoadOneSecret_NonNotFoundSecureErrorPropagates(t *testing.T) {
	g := &stubGetter{errs: map[string]error{
		secureSSMPath + "/" + KeySlackBotToken: &smithy.GenericAPIError{Code: "ThrottlingException"},
	}}
	_, err := loadOneSecret(context.Background(), g, KeySlackBotToken)
	if err == nil {
		t.Fatal("want propagated error")
	}
	if errors.Is(err, ErrSecretNotFound) {
		t.Fatalf("must not fall back to plain on non-NotFound error: %v", err)
	}
}

func TestLoadSecrets_AllKeys(t *testing.T) {
	g := &stubGetter{values: map[string]string{
		secureSSMPath + "/" + KeyAmazonPartnerTag:     "tag",
		secureSSMPath + "/" + KeySlackBotToken:        "slack-token",
		secureSSMPath + "/" + KeySlackNoticeChannel:   "C1",
		plainSSMPath + "/" + KeySlackErrorChannel:     "C2",
		secureSSMPath + "/" + KeyMastodonServer:       "https://mstdn.example",
		secureSSMPath + "/" + KeyMastodonClientID:     "cid",
		secureSSMPath + "/" + KeyMastodonClientSecret: "csec",
		secureSSMPath + "/" + KeyMastodonAccessToken:  "atoken",
		secureSSMPath + "/" + KeyGitHubToken:          "ghtoken",
	}}
	secrets, err := LoadSecrets(context.Background(), g, AllSecretKeys, nil)
	if err != nil {
		t.Fatalf("LoadSecrets: %v", err)
	}
	if secrets.AmazonPartnerTag != "tag" || secrets.SlackBotToken != "slack-token" {
		t.Errorf("secrets not mapped: %+v", secrets)
	}
	if secrets.SlackErrorChannel != "C2" {
		t.Errorf("plain fallback not applied: %+v", secrets)
	}
}

// required key が SSM に存在しない場合は起動エラー（SPECIFICATION.md 19 step4）。
func TestLoadSecrets_RequiredMissingErrors(t *testing.T) {
	g := &stubGetter{values: map[string]string{
		secureSSMPath + "/" + KeyAmazonPartnerTag: "tag",
	}}
	_, err := LoadSecrets(context.Background(), g,
		[]string{KeyAmazonPartnerTag, KeyGitHubToken}, nil)
	if err == nil {
		t.Fatal("LoadSecrets should fail when a required key is missing")
	}
}

// optional key は SSM に存在しなくても起動を妨げない（任意通知先の未設定）。存在すれば値が入る。
func TestLoadSecrets_OptionalMissingSucceeds(t *testing.T) {
	g := &stubGetter{values: map[string]string{
		secureSSMPath + "/" + KeyAmazonPartnerTag: "tag",
		secureSSMPath + "/" + KeySlackBotToken:    "token",
	}}
	secrets, err := LoadSecrets(context.Background(), g,
		[]string{KeyAmazonPartnerTag},
		[]string{KeySlackBotToken, KeySlackNoticeChannel, KeyMastodonServer})
	if err != nil {
		t.Fatalf("LoadSecrets with missing optional: %v", err)
	}
	if secrets.AmazonPartnerTag != "tag" {
		t.Errorf("required AmazonPartnerTag = %q, want tag", secrets.AmazonPartnerTag)
	}
	if secrets.SlackBotToken != "token" {
		t.Errorf("present optional SlackBotToken = %q, want token", secrets.SlackBotToken)
	}
	if secrets.SlackNoticeChannel != "" || secrets.MastodonServer != "" {
		t.Errorf("missing optional keys must stay empty: %+v", secrets)
	}
}
