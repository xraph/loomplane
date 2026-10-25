package aws

import (
	"context"
	"errors"
	"fmt"

	sdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/elasticache"
	ectypes "github.com/aws/aws-sdk-go-v2/service/elasticache/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"

	"github.com/xraph/loomplane/internal/clouds/progress"
)

// --- S3 buckets an application keeps its files in ---

// EnsurePrivateBucket creates a bucket nobody outside the account can
// reach, if there isn't one by that name.
func (s *Session) EnsurePrivateBucket(ctx context.Context, name string) error {
	if err := s.EnsureBucket(ctx, name); err != nil {
		return err
	}
	_, err := s.s3().PutPublicAccessBlock(ctx, &s3.PutPublicAccessBlockInput{
		Bucket: sdk.String(name),
		PublicAccessBlockConfiguration: &s3types.PublicAccessBlockConfiguration{
			BlockPublicAcls: sdk.Bool(true), IgnorePublicAcls: sdk.Bool(true),
			BlockPublicPolicy: sdk.Bool(true), RestrictPublicBuckets: sdk.Bool(true),
		},
	})
	if err != nil {
		return fmt.Errorf("make bucket %s private: %w", name, err)
	}
	return nil
}

// BucketExists reports whether the bucket is there.
func (s *Session) BucketExists(ctx context.Context, name string) (bool, error) {
	_, err := s.s3().HeadBucket(ctx, &s3.HeadBucketInput{Bucket: sdk.String(name)})
	if err == nil {
		return true, nil
	}
	var nf *s3types.NotFound
	if errors.As(err, &nf) || IsNotFound(err) {
		return false, nil
	}
	return false, fmt.Errorf("read bucket %s: %w", name, err)
}

// DeleteBucket deletes a bucket and everything in it. One that's already
// gone is fine.
func (s *Session) DeleteBucket(ctx context.Context, name string) error {
	cli := s.s3()
	pages := s3.NewListObjectsV2Paginator(cli, &s3.ListObjectsV2Input{Bucket: sdk.String(name)})
	for pages.HasMorePages() {
		page, err := pages.NextPage(ctx)
		if IsNotFound(err) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("list bucket %s: %w", name, err)
		}
		if len(page.Contents) == 0 {
			continue
		}
		objects := make([]s3types.ObjectIdentifier, 0, len(page.Contents))
		for _, o := range page.Contents {
			objects = append(objects, s3types.ObjectIdentifier{Key: o.Key})
		}
		if _, err := cli.DeleteObjects(ctx, &s3.DeleteObjectsInput{
			Bucket: sdk.String(name), Delete: &s3types.Delete{Objects: objects, Quiet: sdk.Bool(true)},
		}); err != nil {
			return fmt.Errorf("empty bucket %s: %w", name, err)
		}
	}
	if _, err := cli.DeleteBucket(ctx, &s3.DeleteBucketInput{Bucket: sdk.String(name)}); err != nil && !IsNotFound(err) {
		return fmt.Errorf("delete bucket %s: %w", name, err)
	}
	return nil
}

// --- ElastiCache ---

// EnsureCacheSubnetGroup creates a cache subnet group over subnets, if
// there isn't one by that name.
func (s *Session) EnsureCacheSubnetGroup(ctx context.Context, name string, subnets []string) error {
	_, err := elasticache.NewFromConfig(s.cfg).CreateCacheSubnetGroup(ctx, &elasticache.CreateCacheSubnetGroupInput{
		CacheSubnetGroupName: sdk.String(name), CacheSubnetGroupDescription: sdk.String("Loomplane caches"), SubnetIds: subnets,
	})
	if err != nil && !IsExists(err) {
		return fmt.Errorf("create cache subnet group %s: %w", name, err)
	}
	return nil
}

// CacheSpec is a Redis replication group Loomplane creates.
type CacheSpec struct {
	ID          string
	NodeType    string
	AuthToken   string
	SubnetGroup string
	Group       string
}

// Cache is a Redis replication group as it is.
type Cache struct {
	ID      string
	Status  string
	Address string
	Port    int32
}

// GetCache reads a replication group. It returns an error IsNotFound
// recognises when there isn't one.
func (s *Session) GetCache(ctx context.Context, id string) (*Cache, error) {
	out, err := elasticache.NewFromConfig(s.cfg).DescribeReplicationGroups(ctx, &elasticache.DescribeReplicationGroupsInput{ReplicationGroupId: sdk.String(id)})
	if err != nil {
		return nil, err
	}
	if len(out.ReplicationGroups) == 0 {
		return nil, fmt.Errorf("cache %s: %w", id, ErrNotFound)
	}
	g := out.ReplicationGroups[0]
	c := &Cache{ID: sdk.ToString(g.ReplicationGroupId), Status: sdk.ToString(g.Status)}
	if len(g.NodeGroups) > 0 && g.NodeGroups[0].PrimaryEndpoint != nil {
		c.Address, c.Port = sdk.ToString(g.NodeGroups[0].PrimaryEndpoint.Address), sdk.ToInt32(g.NodeGroups[0].PrimaryEndpoint.Port)
	}
	return c, nil
}

// EnsureCache creates the replication group if there isn't one, with TLS
// and the auth token, and waits until it's available.
func (s *Session) EnsureCache(ctx context.Context, spec CacheSpec) (*Cache, error) {
	if _, err := s.GetCache(ctx, spec.ID); IsNotFound(err) {
		_, err = elasticache.NewFromConfig(s.cfg).CreateReplicationGroup(ctx, &elasticache.CreateReplicationGroupInput{
			ReplicationGroupId: sdk.String(spec.ID), ReplicationGroupDescription: sdk.String("Created by Loomplane"),
			Engine: sdk.String("redis"), CacheNodeType: sdk.String(spec.NodeType), NumCacheClusters: sdk.Int32(1),
			TransitEncryptionEnabled: sdk.Bool(true), AtRestEncryptionEnabled: sdk.Bool(true), AuthToken: sdk.String(spec.AuthToken),
			CacheSubnetGroupName: sdk.String(spec.SubnetGroup), SecurityGroupIds: []string{spec.Group},
		})
		if err != nil && !IsExists(err) {
			return nil, fmt.Errorf("create cache %s: %w", spec.ID, err)
		}
	} else if err != nil {
		return nil, fmt.Errorf("read cache %s: %w", spec.ID, err)
	}
	for {
		c, err := s.GetCache(ctx, spec.ID)
		if err != nil {
			return nil, fmt.Errorf("read cache %s: %w", spec.ID, err)
		}
		switch c.Status {
		case "available":
			return c, nil
		case "create-failed", "deleting":
			return nil, fmt.Errorf("cache %s is %s", spec.ID, c.Status)
		}
		progress.Report(ctx, "The cache is "+c.Status)
		if werr := s.wait(ctx); werr != nil {
			return nil, werr
		}
	}
}

// DeleteCache deletes a replication group and its data. One that's
// already gone is fine.
func (s *Session) DeleteCache(ctx context.Context, id string) error {
	_, err := elasticache.NewFromConfig(s.cfg).DeleteReplicationGroup(ctx, &elasticache.DeleteReplicationGroupInput{
		ReplicationGroupId: sdk.String(id),
	})
	var nf *ectypes.ReplicationGroupNotFoundFault
	if err != nil && !errors.As(err, &nf) && !IsNotFound(err) {
		return fmt.Errorf("delete cache %s: %w", id, err)
	}
	return nil
}
