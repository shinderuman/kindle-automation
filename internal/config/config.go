// Package config は起動時設定の読み取りと validation を担う（SPECIFICATION.md 8/16/19）。
//
// 構成は3つに分かれる。
//   - Env: S3 bucket/key、SQS queue URL、log level を環境変数から集約する（8）。
//   - CheckerConfigs: checker_configs.json を decode し、閾値と Gist 設定を validation する（16）。
//   - Secrets: Slack/Mastodon/GitHub 等の秘密情報を SSM から個別取得する（19）。
//
// 環境変数を domain 層から参照させないため、本 package で構造体へ集約し、呼び出し側へ渡す。
// 不正値をデフォルト値で暗黙に補完しない（SPECIFICATION.md 8/16）。
package config

import (
	"fmt"

	"github.com/shinderuman/kindle-automation/internal/logging"
)

// 環境変数名（SAM テンプレートから Lambda 環境変数へ渡される）。
const (
	EnvS3Bucket                 = "S3_BUCKET"
	EnvS3Region                 = "S3_REGION"
	EnvUnprocessedKey           = "S3_UNPROCESSED_OBJECT_KEY"
	EnvPaperBooksKey            = "S3_PAPER_BOOKS_OBJECT_KEY"
	EnvAuthorsKey               = "S3_AUTHORS_OBJECT_KEY"
	EnvExcludedTitleKeywordsKey = "S3_EXCLUDED_TITLE_KEYWORDS_OBJECT_KEY"
	EnvNotifiedKey              = "S3_NOTIFIED_OBJECT_KEY"
	EnvUpcomingKey              = "S3_UPCOMING_OBJECT_KEY"
	EnvCheckerConfigKey         = "S3_CHECKER_CONFIG_OBJECT_KEY"
	EnvQueueURL                 = "SQS_QUEUE_URL"
	EnvLogLevel                 = "LOG_LEVEL"
)

// Env は環境変数由来の起動設定。S3 object key と queue URL と log level を集約する。
type Env struct {
	S3Bucket                 string
	S3Region                 string
	UnprocessedKey           string
	PaperBooksKey            string
	AuthorsKey               string
	ExcludedTitleKeywordsKey string
	NotifiedKey              string
	UpcomingKey              string
	CheckerConfigKey         string
	QueueURL                 string
	LogLevel                 string
}

// LoadEnv は getenv から環境変数を読み取り validation する。
// 必須項目は空を許さず、log level は空なら INFO、非空なら有効な値を要求する。
func LoadEnv(getenv func(string) string) (Env, error) {
	env := Env{
		S3Bucket:                 getenv(EnvS3Bucket),
		S3Region:                 getenv(EnvS3Region),
		UnprocessedKey:           getenv(EnvUnprocessedKey),
		PaperBooksKey:            getenv(EnvPaperBooksKey),
		AuthorsKey:               getenv(EnvAuthorsKey),
		ExcludedTitleKeywordsKey: getenv(EnvExcludedTitleKeywordsKey),
		NotifiedKey:              getenv(EnvNotifiedKey),
		UpcomingKey:              getenv(EnvUpcomingKey),
		CheckerConfigKey:         getenv(EnvCheckerConfigKey),
		QueueURL:                 getenv(EnvQueueURL),
		LogLevel:                 getenv(EnvLogLevel),
	}
	for _, item := range []struct{ name, value string }{
		{EnvS3Bucket, env.S3Bucket},
		{EnvS3Region, env.S3Region},
		{EnvUnprocessedKey, env.UnprocessedKey},
		{EnvPaperBooksKey, env.PaperBooksKey},
		{EnvAuthorsKey, env.AuthorsKey},
		{EnvExcludedTitleKeywordsKey, env.ExcludedTitleKeywordsKey},
		{EnvNotifiedKey, env.NotifiedKey},
		{EnvUpcomingKey, env.UpcomingKey},
		{EnvCheckerConfigKey, env.CheckerConfigKey},
		{EnvQueueURL, env.QueueURL},
	} {
		if item.value == "" {
			return Env{}, fmt.Errorf("env %s is required", item.name)
		}
	}
	// log level は空なら INFO。非空の場合は有効な値を要求し、不正値を暗黙に補完しない。
	level := env.LogLevel
	if level == "" {
		level = "INFO"
	}
	if _, err := logging.ParseLevel(level); err != nil {
		return Env{}, err
	}
	env.LogLevel = level
	return env, nil
}
