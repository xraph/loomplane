package platform

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	groveext "github.com/xraph/grove/extension"
	"github.com/xraph/trove/driver"
	_ "github.com/xraph/trove/drivers/localdriver" // registers local file storage
	_ "github.com/xraph/trove/drivers/memdriver"   // registers test storage
	_ "github.com/xraph/trove/drivers/s3driver"    // registers S3 and compatible endpoints
	troveext "github.com/xraph/trove/extension"

	"github.com/xraph/loomplane/internal/artifacts"
	"github.com/xraph/loomplane/internal/config"
)

// filesExtension keeps Trove in the central migration and lifecycle order.
type filesExtension struct {
	*troveext.Extension
	buckets map[string]string
}

func (e *filesExtension) Dependencies() []string { return []string{groveext.ExtensionName} }

func (e *filesExtension) Start(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	for name, bucket := range e.buckets {
		store, err := e.Manager().Get(name)
		if err != nil {
			return err
		}
		if store.Driver().Name() == "s3" {
			// Remote buckets are provisioned separately; a runtime role needs
			// access only to its bucket, not account-wide bucket creation.
			objects, listErr := store.List(ctx, bucket, driver.WithMaxKeys(1))
			if listErr != nil {
				return fmt.Errorf("check %s bucket: %w", name, listErr)
			}
			if closeErr := objects.Close(); closeErr != nil {
				return closeErr
			}
		} else if err = store.CreateBucket(ctx, bucket); err != nil && !errors.Is(err, driver.ErrBucketExists) {
			return fmt.Errorf("create %s bucket: %w", name, err)
		}
	}
	return e.Extension.Start(ctx)
}

func newFiles(cfg config.Config) *filesExtension {
	url := cfg.Storage.URL
	if url == "" {
		dir, err := filepath.Abs(filepath.Join(".loomplane", "storage"))
		if err != nil {
			dir = filepath.Join(os.TempDir(), "loomplane-storage")
		}
		url = "file://" + filepath.ToSlash(dir)
	}
	backupURL := cfg.Storage.BackupURL
	if backupURL == "" {
		backupURL = url
	}
	artifactBucket, backupBucket := cfg.Storage.ArtifactBucket, cfg.Storage.BackupBucket
	if artifactBucket == "" {
		artifactBucket = artifacts.Bucket
	}
	if backupBucket == "" {
		backupBucket = "loomplane-backups"
	}
	return &filesExtension{
		Extension: troveext.New(troveext.WithConfig(troveext.Config{
			DisableRoutes: true, DisableMigrate: true,
			Stores: []troveext.FileStoreConfig{
				{Name: "artifacts", StorageDriver: url, DefaultBucket: artifactBucket},
				{Name: "backups", StorageDriver: backupURL, DefaultBucket: backupBucket},
			}, Default: "artifacts",
		})),
		buckets: map[string]string{"artifacts": artifactBucket, "backups": backupBucket},
	}
}
