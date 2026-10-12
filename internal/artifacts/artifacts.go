// Package artifacts keeps what a deployment is made from, such as the
// tarball of the commit it deploys, in Trove. Deployments name an
// artifact by key and read it back to hand to a builder, so every builder
// gets the same bytes.
package artifacts

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"

	"github.com/xraph/trove"
	"github.com/xraph/trove/driver"
	"github.com/xraph/trove/drivers/localdriver"
	"github.com/xraph/trove/drivers/memdriver"
)

// Bucket is where Loomplane's artifacts go.
const Bucket = "loomplane-artifacts"

// ErrNotFound is an artifact that isn't stored.
var ErrNotFound = errors.New("artifact not found")

// Store is Trove, opened on one bucket.
type Store struct {
	t *trove.Trove
}

// Open opens the storage a URL names: file:///absolute/path for a local
// directory, or mem:// for tests. Other schemes need their Trove driver
// registered (s3, gcs) and are looked up by scheme.
func Open(ctx context.Context, dsn string) (*Store, error) {
	u, err := url.Parse(dsn)
	if err != nil {
		return nil, fmt.Errorf("storage url: %w", err)
	}
	var drv driver.Driver
	switch u.Scheme {
	case "file", "local":
		drv = localdriver.New()
	case "mem":
		drv = memdriver.New()
	default:
		factory, ok := driver.Lookup(u.Scheme)
		if !ok {
			return nil, fmt.Errorf("storage url: no Trove driver for %q; use file:// or register one", u.Scheme)
		}
		drv = factory()
	}
	if err = drv.Open(ctx, dsn); err != nil {
		return nil, fmt.Errorf("open storage: %w", err)
	}
	t, err := trove.Open(drv, trove.WithDefaultBucket(Bucket))
	if err != nil {
		return nil, fmt.Errorf("open storage: %w", err)
	}
	if err := t.CreateBucket(ctx, Bucket); err != nil && !errors.Is(err, driver.ErrBucketExists) {
		return nil, fmt.Errorf("create bucket: %w", err)
	}
	return &Store{t: t}, nil
}

// Put stores r under key, replacing what was there, and returns its size.
func (s *Store) Put(ctx context.Context, key string, r io.Reader, contentType string) (int64, error) {
	info, err := s.t.Put(ctx, Bucket, key, r, driver.WithContentType(contentType))
	if err != nil {
		return 0, fmt.Errorf("store artifact: %w", err)
	}
	return info.Size, nil
}

// Open reads an artifact. The caller closes it.
func (s *Store) Open(ctx context.Context, key string) (io.ReadCloser, int64, error) {
	obj, err := s.t.Get(ctx, Bucket, key)
	if errors.Is(err, trove.ErrNotFound) {
		return nil, 0, ErrNotFound
	}
	if err != nil {
		return nil, 0, fmt.Errorf("read artifact: %w", err)
	}
	var size int64
	if obj.Info != nil {
		size = obj.Info.Size
	}
	return obj.ReadCloser, size, nil
}

// Exists reports whether key is stored.
func (s *Store) Exists(ctx context.Context, key string) (bool, error) {
	_, err := s.t.Head(ctx, Bucket, key)
	if errors.Is(err, trove.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("check artifact: %w", err)
	}
	return true, nil
}

// Close releases the storage.
func (s *Store) Close(ctx context.Context) error { return s.t.Close(ctx) }
