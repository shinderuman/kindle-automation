package config

import (
	"context"
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/aws/smithy-go"
)

// SSM parameter path prefix。既存 kindle_bot と同じ prefix を使用する（SPECIFICATION.md 19）。
const (
	plainSSMPath  = "/myapp/plain"
	secureSSMPath = "/myapp/secure"
)

// 取得する SSM parameter key（SPECIFICATION.md 19）。PA API 資格情報は含めない。
const (
	KeyAmazonPartnerTag     = "AMAZON_PARTNER_TAG"
	KeySlackBotToken        = "SLACK_BOT_TOKEN"
	KeySlackNoticeChannel   = "SLACK_NOTICE_CHANNEL"
	KeySlackErrorChannel    = "SLACK_ERROR_CHANNEL"
	KeyMastodonServer       = "MASTODON_SERVER"
	KeyMastodonClientID     = "MASTODON_CLIENT_ID"
	KeyMastodonClientSecret = "MASTODON_CLIENT_SECRET"
	KeyMastodonAccessToken  = "MASTODON_ACCESS_TOKEN"
	KeyGitHubToken          = "GITHUB_TOKEN"
)

// AllSecretKeys は新システムが使用する SSM parameter key の全集。
var AllSecretKeys = []string{
	KeyAmazonPartnerTag,
	KeySlackBotToken,
	KeySlackNoticeChannel,
	KeySlackErrorChannel,
	KeyMastodonServer,
	KeyMastodonClientID,
	KeyMastodonClientSecret,
	KeyMastodonAccessToken,
	KeyGitHubToken,
}

// ErrSecretNotFound は secure/plain 両方に parameter がなかった（SPECIFICATION.md 19 step4）。
var ErrSecretNotFound = errors.New("secret not found in secure or plain")

// Secrets は SSM から取得した秘密情報。各 adapter ぀配布される。
type Secrets struct {
	AmazonPartnerTag     string
	SlackBotToken        string
	SlackNoticeChannel   string
	SlackErrorChannel    string
	MastodonServer       string
	MastodonClientID     string
	MastodonClientSecret string
	MastodonAccessToken  string
	GitHubToken          string
}

// ParameterGetter は SSM GetParameter の必要部分だけを取り出した interface。
// *ssm.Client が満たす。テストでは手書き stub へ差し替える。
type ParameterGetter interface {
	GetParameter(ctx context.Context, in *ssm.GetParameterInput, opts ...func(*ssm.Options)) (*ssm.GetParameterOutput, error)
}

// LoadSecrets は required keys を /myapp/secure/{KEY}(WithDecryption) → /myapp/plain/{KEY} の順で
// 必須取得し、optional keys を任意取得して Secrets を構築する（SPECIFICATION.md 19）。
// 両方に存在する場合は secure 側を使用する。
// required は両方になければ起動エラーとする。optional は存在しなければ空文字のままとし error としない
// （Slack/Mastodon 等の任意通知先）。secure/plain 判定以外の SSM エラーは error として伝播する。
// GetParametersByPath は使わず、必要な key だけを個別取得する。
func LoadSecrets(ctx context.Context, getter ParameterGetter, required, optional []string) (Secrets, error) {
	values := make(map[string]string, len(required)+len(optional))
	for _, key := range required {
		v, err := loadOneSecret(ctx, getter, key)
		if err != nil {
			return Secrets{}, fmt.Errorf("load secret %s: %w", key, err)
		}
		values[key] = v
	}
	for _, key := range optional {
		v, err := loadOneSecret(ctx, getter, key)
		if err != nil {
			if errors.Is(err, ErrSecretNotFound) {
				continue
			}
			return Secrets{}, fmt.Errorf("load optional secret %s: %w", key, err)
		}
		values[key] = v
	}
	return secretsFromMap(values), nil
}

// loadOneSecret は secure を先に試し、ParameterNotFound だけ plain へ fallback する。
func loadOneSecret(ctx context.Context, getter ParameterGetter, key string) (string, error) {
	if v, err := getParameter(ctx, getter, secureSSMPath+"/"+key, true); err == nil {
		return v, nil
	} else if !isParameterNotFound(err) {
		return "", err
	}
	v, err := getParameter(ctx, getter, plainSSMPath+"/"+key, false)
	if err != nil {
		if isParameterNotFound(err) {
			return "", fmt.Errorf("%w: %s", ErrSecretNotFound, key)
		}
		return "", err
	}
	return v, nil
}

func getParameter(ctx context.Context, getter ParameterGetter, name string, decrypt bool) (string, error) {
	out, err := getter.GetParameter(ctx, &ssm.GetParameterInput{
		Name:           aws.String(name),
		WithDecryption: aws.Bool(decrypt),
	})
	if err != nil {
		return "", err
	}
	if out.Parameter == nil || out.Parameter.Value == nil {
		return "", fmt.Errorf("parameter %s has no value", name)
	}
	return *out.Parameter.Value, nil
}

// isParameterNotFound は SSM の ParameterNotFound エラーかを判定する。
// 生成された具象型へ依存せず smithy.APIError の code で判定する。
func isParameterNotFound(err error) bool {
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) && apiErr.ErrorCode() == "ParameterNotFound" {
		return true
	}
	return false
}

func secretsFromMap(m map[string]string) Secrets {
	return Secrets{
		AmazonPartnerTag:     m[KeyAmazonPartnerTag],
		SlackBotToken:        m[KeySlackBotToken],
		SlackNoticeChannel:   m[KeySlackNoticeChannel],
		SlackErrorChannel:    m[KeySlackErrorChannel],
		MastodonServer:       m[KeyMastodonServer],
		MastodonClientID:     m[KeyMastodonClientID],
		MastodonClientSecret: m[KeyMastodonClientSecret],
		MastodonAccessToken:  m[KeyMastodonAccessToken],
		GitHubToken:          m[KeyGitHubToken],
	}
}
