package aws

import (
	"context"
	"errors"
	"strings"
	"time"

	sdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/arn"
	"github.com/aws/aws-sdk-go-v2/service/elasticache"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/xraph/loomplane/pkg/provider"
)

// ResourceInventory reads RDS, replication groups and general purpose buckets in one region.
func (s *Session) ResourceInventory(ctx context.Context) (provider.ResourceInventory, error) {
	out := provider.ResourceInventory{Scope: "account/" + s.account + "/regions/" + s.region + "/managed-resources", Resources: []provider.ResourceObservation{}}
	fail := func() (provider.ResourceInventory, error) {
		return out, errors.New("AWS resource inventory could not be read completely")
	}
	if s.account == "" || s.region == "" {
		return fail()
	}
	ids := map[string]bool{}
	valid := func(id, service string) bool {
		a, e := arn.Parse(id)
		return e == nil && a.AccountID == s.account && a.Region == s.region && a.Service == service && !ids[id]
	}
	marker := ""
	seen := map[string]bool{}
	rd := rds.NewFromConfig(s.cfg)
	for n := 0; n < 100; n++ {
		if seen[marker] {
			return fail()
		}
		seen[marker] = true
		in := &rds.DescribeDBInstancesInput{MaxRecords: sdk.Int32(100)}
		if marker != "" {
			in.Marker = sdk.String(marker)
		}
		page, err := rd.DescribeDBInstances(ctx, in)
		if err != nil {
			return fail()
		}
		for _, d := range page.DBInstances {
			kind := sdk.ToString(d.Engine)
			if kind != "postgres" && kind != "mysql" {
				continue
			}
			id := sdk.ToString(d.DBInstanceArn)
			if !valid(id, "rds") {
				return fail()
			}
			ids[id] = true
			r := provider.ResourceObservation{ID: id, Incarnation: sdk.ToString(d.DbiResourceId), Name: sdk.ToString(d.DBInstanceIdentifier), Kind: kind, Version: sdk.ToString(d.EngineVersion), Region: s.region, Status: sdk.ToString(d.DBInstanceStatus), Database: sdk.ToString(d.DBName)}
			if d.Endpoint != nil {
				r.Host, r.Port = sdk.ToString(d.Endpoint.Address), int(sdk.ToInt32(d.Endpoint.Port))
			}
			out.Resources = append(out.Resources, r)
		}
		marker = sdk.ToString(page.Marker)
		if marker == "" {
			break
		}
		if n == 99 {
			return fail()
		}
	}
	marker = ""
	seen = map[string]bool{}
	ec := elasticache.NewFromConfig(s.cfg)
	for n := 0; n < 100; n++ {
		if seen[marker] {
			return fail()
		}
		seen[marker] = true
		in := &elasticache.DescribeReplicationGroupsInput{MaxRecords: sdk.Int32(100)}
		if marker != "" {
			in.Marker = sdk.String(marker)
		}
		page, err := ec.DescribeReplicationGroups(ctx, in)
		if err != nil {
			return fail()
		}
		for _, d := range page.ReplicationGroups {
			id := sdk.ToString(d.ARN)
			if !valid(id, "elasticache") {
				return fail()
			}
			ids[id] = true
			r := provider.ResourceObservation{ID: id, Name: sdk.ToString(d.ReplicationGroupId), Kind: "redis", Region: s.region, Status: sdk.ToString(d.Status)}
			if d.ReplicationGroupCreateTime != nil {
				r.Incarnation = d.ReplicationGroupCreateTime.UTC().Format(time.RFC3339Nano)
			}
			endpoint := d.ConfigurationEndpoint
			if endpoint == nil && len(d.NodeGroups) > 0 {
				endpoint = d.NodeGroups[0].PrimaryEndpoint
			}
			if endpoint != nil {
				r.Host, r.Port = sdk.ToString(endpoint.Address), int(sdk.ToInt32(endpoint.Port))
			}
			out.Resources = append(out.Resources, r)
		}
		marker = sdk.ToString(page.Marker)
		if marker == "" {
			break
		}
		if n == 99 {
			return fail()
		}
	}
	marker = ""
	seen = map[string]bool{}
	sc := s.s3()
	for n := 0; n < 100; n++ {
		if seen[marker] {
			return fail()
		}
		seen[marker] = true
		in := &s3.ListBucketsInput{BucketRegion: sdk.String(s.region), MaxBuckets: sdk.Int32(100)}
		if marker != "" {
			in.ContinuationToken = sdk.String(marker)
		}
		page, err := sc.ListBuckets(ctx, in)
		if err != nil {
			return fail()
		}
		for _, b := range page.Buckets {
			name := sdk.ToString(b.Name)
			region := sdk.ToString(b.BucketRegion)
			id := "arn:aws:s3:::" + name
			if name == "" || strings.ContainsAny(name, "/\\?#") || region != s.region || ids[id] {
				return fail()
			}
			ids[id] = true
			r := provider.ResourceObservation{ID: id, Name: name, Kind: "object-storage", Region: region}
			// S3 CreationDate can change after policy edits, so it cannot prove an incarnation.
			out.Resources = append(out.Resources, r)
		}
		marker = sdk.ToString(page.ContinuationToken)
		if marker == "" {
			break
		}
		if n == 99 {
			return fail()
		}
	}
	out.Complete = true
	return out, nil
}
