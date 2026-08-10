package checkworker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/aws/aws-lambda-go/lambda"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/ssm"

	"github.com/shinderuman/kindle-automation/internal/amazon"
	"github.com/shinderuman/kindle-automation/internal/application/newrelease"
	"github.com/shinderuman/kindle-automation/internal/application/papertokindle"
	"github.com/shinderuman/kindle-automation/internal/application/sale"
	"github.com/shinderuman/kindle-automation/internal/config"
	"github.com/shinderuman/kindle-automation/internal/gist"
	"github.com/shinderuman/kindle-automation/internal/logging"
	"github.com/shinderuman/kindle-automation/internal/notification"
	"github.com/shinderuman/kindle-automation/internal/queue"
	"github.com/shinderuman/kindle-automation/internal/storage"
)

// checkWorkerRequiredSecretKeys は check-worker Lambda が起動に必須な SSM secret key（SPECIFICATION.md 19）。
// Amazon affiliate tag・Gist 更新 token に加え、商品通知で必ず使用する Slack notice・Mastodon の4値を
// required とする。secure/plain いずれにも存在しないか空値なら LoadSecrets が起動エラーにする。
// SLACK_ERROR_CHANNEL（schedule-checks 専用）・MASTODON_CLIENT_ID/SECRET（check-worker 不使用）は含めない。
var checkWorkerRequiredSecretKeys = []string{
	config.KeyAmazonPartnerTag,
	config.KeyGitHubToken,
	config.KeySlackBotToken,
	config.KeySlackNoticeChannel,
	config.KeyMastodonServer,
	config.KeyMastodonAccessToken,
}

// Start は check-worker Lambda のエントリポイント。依存を組み立て Lambda runtime へ登録する。
// 起動時の依存組み立て・validation 失敗は継続不能のため標準エラーへ出力し非0で終了する。
func Start() {
	worker, err := buildWorker(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, "check-worker init failed:", err)
		os.Exit(1)
	}
	lambda.Start(worker.HandleSQSEvent)
}

// buildWorker は環境変数・S3・SSM から依存を組み立てて Worker を返す。
// 起動時 validation に失敗した場合は error を返し、呼び出し側で Lambda 起動失敗とする。
func buildWorker(ctx context.Context) (*Worker, error) {
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

	secrets, err := config.LoadSecrets(ctx, ssmClient, checkWorkerRequiredSecretKeys, nil)
	if err != nil {
		return nil, fmt.Errorf("load secrets: %w", err)
	}

	amazonClient := amazon.NewClient()
	nrFetcher := newReleaseFetcher{client: amazonClient, partnerTag: secrets.AmazonPartnerTag}
	enqueuer := queue.NewEnqueuer(sqsClient, env.QueueURL, logger)
	notifier := buildNotifier(secrets, logger)
	clock := time.Now

	return &Worker{
		SaleDeps: sale.Dependencies{
			Fetcher:  saleFetcher{client: amazonClient},
			Store:    saleBookStore{inner: storage.NewBookFileStore(store, env.UnprocessedKey)},
			Notifier: notifier,
			Enqueuer: enqueuer,
			Config: sale.Config{
				UnprocessedKey: env.UnprocessedKey,
			},
			Clock: clock,
		},
		NRDeps: newrelease.Dependencies{
			SearchFetcher:  nrFetcher,
			ProductFetcher: nrFetcher,
			NotifiedStore:  storage.NewBookFileStore(store, env.NotifiedKey),
			UpcomingStore:  storage.NewBookFileStore(store, env.UpcomingKey),
			AuthorStore:    storage.NewAuthorFileStore(store, env.AuthorsKey),
			Enqueuer:       enqueuer,
			Notifier:       notifier,
			Config:         newrelease.Config{},
			Clock:          clock,
		},
		PaperDeps: papertokindle.Dependencies{
			PaperPageFetcher:  paperPageFetcher{client: amazonClient},
			KindlePageFetcher: kindlePageFetcher{client: amazonClient},
			PaperBooksStore:   paperBooksStore{inner: storage.NewBookFileStore(store, env.PaperBooksKey)},
			NotifiedStore:     storage.NewBookFileStore(store, env.NotifiedKey),
			UpcomingStore:     storage.NewBookFileStore(store, env.UpcomingKey),
			KnownStateQuerier: paperKnownStateQuerier{
				inner: storage.NewKnownStateQuerier(store, env.NotifiedKey, env.UpcomingKey, env.UnprocessedKey, env.PaperBooksKey),
			},
			Enqueuer: enqueuer,
			Notifier: notifier,
			Config:   papertokindle.Config{PartnerTag: secrets.AmazonPartnerTag},
			Clock:    clock,
		},
		GistDeps: gist.Dependencies{
			SaleBooks:  storage.NewBookFileStore(store, env.UnprocessedKey),
			PaperBooks: storage.NewBookFileStore(store, env.PaperBooksKey),
			Authors:    storage.NewAuthorFileStore(store, env.AuthorsKey),
			Updater:    gist.NewGitHubClient(secrets.GitHubToken),
		},
		// checker_configs.json・excluded_title_keywords.json は手動更新され得る可変設定のため
		// invocation ごとに最新値を読む（refreshVariableConfig）。store/key は不変で cold start 再利用。
		store:                    store,
		checkerConfigKey:         env.CheckerConfigKey,
		excludedTitleKeywordsKey: env.ExcludedTitleKeywordsKey,
		Logger:                   logger,
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

// loadExcludedKeywords は excluded_title_keywords.json（文字列配列）を読み取る。
// object が存在しない場合は除外語なし（空）とする。
func loadExcludedKeywords(ctx context.Context, store storage.ObjectStore, key string) ([]string, error) {
	obj, err := store.Get(ctx, key)
	if err != nil {
		if errors.Is(err, storage.ErrObjectNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("get %s: %w", key, err)
	}
	var keywords []string
	if err := json.Unmarshal(obj.Body, &keywords); err != nil {
		return nil, fmt.Errorf("decode %s: %w", key, err)
	}
	return keywords, nil
}

// refreshVariableConfig は checker_configs.json と excluded_title_keywords.json を読み直し、
// 各ユースケース依存の可変設定（SaleThreshold・除外キーワード・Gist 設定）へ反映する。
// warm execution environment の連続 invocation でも次回から S3 の変更を反映するため HandleSQSEvent の先頭で呼ぶ。
// 不変な依存（Fetcher・Store・Notifier・Enqueuer・Clock）はそのまま再利用する。
func (w *Worker) refreshVariableConfig(ctx context.Context) error {
	checker, err := loadCheckerConfigs(ctx, w.store, w.checkerConfigKey)
	if err != nil {
		return err
	}
	excluded, err := loadExcludedKeywords(ctx, w.store, w.excludedTitleKeywordsKey)
	if err != nil {
		return err
	}
	w.SaleDeps.Config.Thresholds = checker.SaleThresholds()
	w.NRDeps.Config.ExcludedKeywords = excluded
	w.GistDeps.Settings = gistSettings(checker)
	return nil
}

// buildNotifier は SSM 秘密情報から Slack・Mastodon 両送信者を構築する。
// 4値は上流の LoadSecrets で required（secure/plain 両方欠落・空値を起動エラー）として検査済みのため、
// ここへ来る時点で全て非空。一部欠落を理由に送信先を黙って無効化せず、両送信先を必ず構築する
// （レビュー指摘2: 必要値欠落での黙る adapter 無効化・正常起動を禁止）。
func buildNotifier(secrets config.Secrets, logger *slog.Logger) *notification.Notifier {
	slack := notification.NewSlackSender(secrets.SlackBotToken, secrets.SlackNoticeChannel)
	mastodon := notification.NewMastodonSender(secrets.MastodonServer, secrets.MastodonAccessToken)
	return notification.NewNotifier(slack, mastodon, logger)
}

// gistSettings は checker 設定から gist_type ごとの更新先を組み立てる。
func gistSettings(checker config.CheckerConfigs) gist.Settings {
	return gist.Settings{
		Sale:          gistTarget(checker, gist.TypeSale),
		NewRelease:    gistTarget(checker, gist.TypeNewRelease),
		PaperToKindle: gistTarget(checker, gist.TypePaperToKindle),
	}
}

func gistTarget(checker config.CheckerConfigs, gistType string) gist.Target {
	id, filename, err := checker.GistMeta(gistType)
	if err != nil {
		// gist_type は固定3種のため GistMeta は失敗しない。失敗は到達不能な継続不能エラー。
		panic(fmt.Sprintf("checker config missing gist meta for %s: %v", gistType, err))
	}
	return gist.Target{ID: id, Filename: filename}
}
