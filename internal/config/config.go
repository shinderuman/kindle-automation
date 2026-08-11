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
	// EnvS3Bucket は対象 S3 bucket 名を受け取る。
	EnvS3Bucket = "S3_BUCKET"
	// EnvS3Region は対象 S3 bucket のリージョンを受け取る。
	EnvS3Region = "S3_REGION"
	// EnvUnprocessedKey は unprocessed_asins.json の object key を受け取る。
	EnvUnprocessedKey = "S3_UNPROCESSED_OBJECT_KEY"
	// EnvPaperBooksKey は paper_books_asins.json の object key を受け取る。
	EnvPaperBooksKey = "S3_PAPER_BOOKS_OBJECT_KEY"
	// EnvAuthorsKey は authors.json の object key を受け取る。
	EnvAuthorsKey = "S3_AUTHORS_OBJECT_KEY"
	// EnvExcludedTitleKeywordsKey は excluded_title_keywords.json の object key を受け取る。
	EnvExcludedTitleKeywordsKey = "S3_EXCLUDED_TITLE_KEYWORDS_OBJECT_KEY"
	// EnvNotifiedKey は notified_asins.json の object key を受け取る。
	EnvNotifiedKey = "S3_NOTIFIED_OBJECT_KEY"
	// EnvUpcomingKey は upcoming_asins.json の object key を受け取る。
	EnvUpcomingKey = "S3_UPCOMING_OBJECT_KEY"
	// EnvCheckerConfigKey は checker_configs.json の object key を受け取る。
	EnvCheckerConfigKey = "S3_CHECKER_CONFIG_OBJECT_KEY"
	// EnvQueueURL はジョブ投入先 SQS queue URL を受け取る。
	EnvQueueURL = "SQS_QUEUE_URL"
	// EnvLogLevel は構造化ログの level 文字列を受け取る（空なら INFO）。
	EnvLogLevel = "LOG_LEVEL"
)

// Env は環境変数から集約した起動設定（SPECIFICATION.md 8）。domain 層へ環境変数名を晒さないためここで構造体へ詰める。
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

// LoadEnv は必須項目は空を許さず、log level は空なら INFO、非空なら有効な値を要求する。
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
