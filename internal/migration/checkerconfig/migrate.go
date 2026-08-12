package checkerconfig

import (
	"context"
	"fmt"

	"github.com/shinderuman/kindle-automation/internal/config"
	"github.com/shinderuman/kindle-automation/internal/storage"
)

func migrateObject(ctx context.Context, store storage.ObjectStore, key string, apply bool) (report, error) {
	obj, err := store.Get(ctx, key)
	if err != nil {
		return report{}, fmt.Errorf("get %s: %w", key, err)
	}
	migrated, rep, err := applyMigration(obj.Body)
	if err != nil {
		return report{}, err
	}
	if err := validateMigrated(migrated); err != nil {
		return report{}, fmt.Errorf("%s: %w", key, err)
	}
	if apply {
		if err := store.Put(ctx, key, migrated, storage.PutOptions{IfMatch: obj.ETag}); err != nil {
			return report{}, fmt.Errorf("put %s: %w", key, err)
		}
	}
	rep.Key = key
	return rep, nil
}

func validateMigrated(body []byte) error {
	checker, err := config.DecodeCheckerConfigs(body)
	if err != nil {
		return fmt.Errorf("decode: %w", err)
	}
	if err := checker.Validate(); err != nil {
		return fmt.Errorf("validate: %w", err)
	}
	return nil
}
