package checkerconfig

import (
	"context"
	"fmt"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/shinderuman/kindle-automation/internal/storage"
)

func newStore(ctx context.Context, region, bucket string) (storage.ObjectStore, error) {
	cfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(region))
	if err != nil {
		return nil, fmt.Errorf("load aws config: %w", err)
	}
	return storage.NewS3Store(s3.NewFromConfig(cfg), bucket), nil
}
