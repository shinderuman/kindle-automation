package schedulechecks

import (
	"context"
	"fmt"
	"os"

	"github.com/aws/aws-lambda-go/lambda"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/ssm"

	"github.com/shinderuman/kindle-automation/internal/application/dispatch"
	"github.com/shinderuman/kindle-automation/internal/config"
	"github.com/shinderuman/kindle-automation/internal/job"
	"github.com/shinderuman/kindle-automation/internal/logging"
	"github.com/shinderuman/kindle-automation/internal/notification"
	"github.com/shinderuman/kindle-automation/internal/queue"
	"github.com/shinderuman/kindle-automation/internal/storage"
)

// scheduleChecksSecretKeys は schedule-checks Lambda が起動に必要な SSM secret key。
// CloudWatch Alarm を Slack error channel へ通知するため Slack Bot Token と Error Channel だけを
// 必須とし、AllSecretKeys のうちそれ以外（Mastodon・GitHub 等）は起動要件としない（SPECIFICATION.md 19）。
var scheduleChecksSecretKeys = []string{
	config.KeySlackBotToken,
	config.KeySlackErrorChannel,
}

// Start は依存組み立て失敗時は標準エラーへ出力し非0で終了する。
func Start() {
	scheduler, err := buildScheduler(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, "schedule-checks init failed:", err)
		os.Exit(1)
	}
	lambda.Start(scheduler.HandleEvent)
}

func buildScheduler(ctx context.Context) (*Scheduler, error) {
	env, err := config.LoadEnv(os.Getenv)
	if err != nil {
		return nil, fmt.Errorf("load env: %w", err)
	}
	level, err := logging.ParseLevel(env.LogLevel)
	if err != nil {
		return nil, err
	}
	logger := logging.New(os.Stdout, level)

	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(env.S3Region))
	if err != nil {
		return nil, fmt.Errorf("load aws config: %w", err)
	}
	s3Client := s3.NewFromConfig(awsCfg)
	sqsClient := sqs.NewFromConfig(awsCfg)
	ssmClient := ssm.NewFromConfig(awsCfg)

	store := storage.NewS3Store(s3Client, env.S3Bucket)

	secrets, err := config.LoadSecrets(ctx, ssmClient, scheduleChecksSecretKeys, nil)
	if err != nil {
		return nil, fmt.Errorf("load secrets: %w", err)
	}

	deps := dispatch.Dependencies{
		AsinListReader: asinListReader{store: store},
		AuthorReader:   authorReader{store: store},
		// checker_configs.json は手動更新され得る可変設定のため、IsEnabled の呼び出しごとに
		// 最新値を読む（cold start に固定しない）。store/key は不変なので cold start 再利用できる。
		ConfigReader: checkerConfigReader{store: store, key: env.CheckerConfigKey},
		Enqueuer:     queue.NewEnqueuer(sqsClient, env.QueueURL, logger),
		Keys: dispatch.Keys{
			Unprocessed: env.UnprocessedKey,
			Authors:     env.AuthorsKey,
			PaperBooks:  env.PaperBooksKey,
		},
	}

	var errorSender notification.Sender
	if secrets.SlackBotToken != "" && secrets.SlackErrorChannel != "" {
		errorSender = notification.NewSlackSender(secrets.SlackBotToken, secrets.SlackErrorChannel)
	}

	return &Scheduler{
		Deps:        deps,
		ErrorSender: errorSender,
		Logger:      logger,
	}, nil
}

func loadCheckerConfigs(ctx context.Context, store storage.ObjectStore, key string) (config.CheckerConfigs, error) {
	obj, err := store.Get(ctx, key)
	if err != nil {
		return config.CheckerConfigs{}, fmt.Errorf("get %s: %w", key, err)
	}
	checker, err := config.DecodeCheckerConfigs(obj.Body)
	if err != nil {
		return config.CheckerConfigs{}, err
	}
	if err := checker.Validate(); err != nil {
		return config.CheckerConfigs{}, err
	}
	return checker, nil
}

// checkerConfigReader は dispatch.ConfigReader への adapter。IsEnabled の呼び出しごとに
// checker_configs.json を読み直すことで、warm execution environment でも更新を次回 invocation へ反映する。
// store/key は不変（cold start 再利用）だが、設定値は毎回最新を読む。
type checkerConfigReader struct {
	store storage.ObjectStore
	key   string
}

// IsEnabled は dispatch.ConfigReader への bridge で、呼び出しごとに checker_configs.json を読んで有効判定を返す。
func (r checkerConfigReader) IsEnabled(ctx context.Context, checkType job.CheckType) (bool, error) {
	checker, err := loadCheckerConfigs(ctx, r.store, r.key)
	if err != nil {
		return false, err
	}
	return checker.IsEnabled(ctx, checkType)
}
