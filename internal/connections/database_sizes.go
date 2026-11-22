package connections

import (
	"context"
	"fmt"
	"regexp"

	"github.com/xraph/loomplane/internal/clouds/aws"
	"github.com/xraph/loomplane/internal/clouds/azure"
	"github.com/xraph/loomplane/internal/clouds/gcp"
	"github.com/xraph/loomplane/internal/security"
)

type DatabaseSizes struct {
	Sizes []string `json:"sizes"`
}

var cacheInstanceName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,59}$`)
var capacityRegion = regexp.MustCompile(`^[a-z][a-z0-9-]{0,40}$`)

func (s *Service) DatabaseSizes(ctx context.Context, p security.Principal, id, kind, region, version, instance string) (DatabaseSizes, error) {
	if err := s.authz.Require(ctx, p, security.InfrastructureRead, ""); err != nil {
		return DatabaseSizes{}, err
	}
	cache := kind == "redis" && cacheInstanceName.MatchString(instance)
	if (!cache && kind != "mysql" && kind != "postgres") || !capacityRegion.MatchString(region) || len(version) > 20 {
		return DatabaseSizes{}, ErrInvalid
	}
	c, err := s.store.GetConnection(ctx, p.OrgID, id)
	if err != nil {
		return DatabaseSizes{}, err
	}
	if c.Status != StatusReady || (c.Cloud != AWS && c.Cloud != GCP && c.Cloud != Azure) || (cache && c.Cloud == GCP) {
		return DatabaseSizes{}, ErrInvalid
	}
	c, secret, err := s.Credential(ctx, p.OrgID, id)
	if err != nil {
		return DatabaseSizes{}, err
	}
	var sizes []string
	switch checker := s.checkers[c.Cloud].(type) {
	case AWSChecker:
		key, e := aws.ParseKey(secret)
		if e != nil || checker.Client == nil || region != c.Details["region"] {
			return DatabaseSizes{}, ErrInvalid
		}
		session := checker.Client.Session(key, region, c.Account)
		if cache {
			sizes, err = session.RedisScaleSizes(ctx, instance)
		} else {
			sizes, err = session.DatabaseClasses(ctx, kind, version)
		}
	case GCPChecker:
		key, e := gcp.ParseKey(secret)
		if e != nil || checker.Client == nil {
			return DatabaseSizes{}, ErrInvalid
		}
		session, e := checker.Client.Session(ctx, key)
		if e != nil {
			return DatabaseSizes{}, ErrUnavailable
		}
		sizes, err = session.DatabaseTiers(ctx, region)
	case AzureChecker:
		cred, e := azure.ParseCredential(secret, c.Details)
		if e != nil || checker.Client == nil {
			return DatabaseSizes{}, ErrInvalid
		}
		session, e := checker.Client.Session(ctx, cred)
		if e != nil {
			return DatabaseSizes{}, ErrUnavailable
		}
		if cache {
			sizes, err = session.RedisScaleSizes(ctx, instance)
		} else {
			sizes, err = session.DatabaseSKUs(ctx, region, kind)
		}
	default:
		return DatabaseSizes{}, ErrUnavailable
	}
	if err != nil {
		return DatabaseSizes{}, fmt.Errorf("%w: the provider could not list database sizes for this account and region", ErrUnavailable)
	}
	if sizes == nil {
		sizes = []string{}
	}
	return DatabaseSizes{Sizes: sizes}, nil
}
