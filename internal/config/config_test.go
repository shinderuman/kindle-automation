package config

import (
	"strings"
	"testing"
)

func envMap(values map[string]string) func(string) string {
	return func(key string) string {
		return values[key]
	}
}

func fullEnv() map[string]string {
	return map[string]string{
		EnvS3Bucket:                 "kindle-asins",
		EnvS3Region:                 "ap-northeast-1",
		EnvUnprocessedKey:           "unprocessed_asins.json",
		EnvPaperBooksKey:            "paper_books_asins.json",
		EnvAuthorsKey:               "authors.json",
		EnvExcludedTitleKeywordsKey: "excluded_title_keywords.json",
		EnvNotifiedKey:              "notified_asins.json",
		EnvUpcomingKey:              "upcoming_asins.json",
		EnvCheckerConfigKey:         "checker_configs.json",
		EnvQueueURL:                 "https://sqs.ap-northeast-1.amazonaws.com/123/jobs.fifo",
		EnvLogLevel:                 "INFO",
	}
}

func TestLoadEnv_Success(t *testing.T) {
	env, err := LoadEnv(envMap(fullEnv()))
	if err != nil {
		t.Fatalf("LoadEnv: %v", err)
	}
	if env.S3Bucket != "kindle-asins" || env.QueueURL == "" || env.AuthorsKey != "authors.json" {
		t.Errorf("env not loaded: %+v", env)
	}
}

func TestLoadEnv_MissingRequired(t *testing.T) {
	values := fullEnv()
	delete(values, EnvS3Bucket)
	_, err := LoadEnv(envMap(values))
	if err == nil || !strings.Contains(err.Error(), EnvS3Bucket) {
		t.Fatalf("want error mentioning %s, got %v", EnvS3Bucket, err)
	}
}

func TestLoadEnv_LogLevelDefaultsToInfo(t *testing.T) {
	values := fullEnv()
	delete(values, EnvLogLevel)
	env, err := LoadEnv(envMap(values))
	if err != nil {
		t.Fatalf("LoadEnv without log level: %v", err)
	}
	if env.LogLevel != "INFO" {
		t.Errorf("LogLevel = %q, want INFO", env.LogLevel)
	}
}

func TestLoadEnv_RejectsInvalidLogLevel(t *testing.T) {
	values := fullEnv()
	values[EnvLogLevel] = "VERBOSE"
	if _, err := LoadEnv(envMap(values)); err == nil {
		t.Fatal("want error for invalid log level")
	}
}
