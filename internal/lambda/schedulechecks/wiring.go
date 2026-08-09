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
	"github.com/shinderuman/kindle-automation/internal/logging"
	"github.com/shinderuman/kindle-automation/internal/notification"
	"github.com/shinderuman/kindle-automation/internal/queue"
	"github.com/shinderuman/kindle-automation/internal/storage"
)

// Start は schedule-checks Lambda のエントリポイント。依存を組み立て Lambda runtime へ登録する。
// 起動時の依存組み立て失敗は継続不能のため標準エラーへ出力し非0で終了する。
func Start() {
	scheduler, err := buildScheduler(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, "schedule-checks init failed:", err)
		os.Exit(1)
	}
	lambda.Start(scheduler.HandleEvent)
}

// buildScheduler は環境変数・S3・SSM から依存を組み立てて Scheduler を返す。
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

	secrets, err := config.LoadSecrets(ctx, ssmClient, config.AllSecretKeys)
	if err != nil {
		return nil, fmt.Errorf("load secrets: %w", err)
	}
	checker, err := loadCheckerConfigs(ctx, store, env.CheckerConfigKey)
	if err != nil {
		return nil, err
	}

	deps := dispatch.Dependencies{
		AsinListReader: asinListReader{store: store},
		AuthorReader:   authorReader{store: store},
		ConfigReader:   checker,
		Enqueuer:       queue.NewEnqueuer(sqsClient, env.QueueURL, logger),
		UpcomingMerger: upcomingMerger{store: store, unprocessedKey: env.UnprocessedKey, upcomingKey: env.UpcomingKey},
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

// loadCheckerConfigs は checker_configs.json を読み取り validation する。
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
