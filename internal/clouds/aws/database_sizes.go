package aws

import (
	"context"
	"maps"
	"slices"
	"strings"

	sdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/elasticache"
	"github.com/aws/aws-sdk-go-v2/service/rds"
)

func (s *Session) DatabaseClasses(ctx context.Context, engine, major string) ([]string, error) {
	classes := map[string]bool{}
	pages := rds.NewDescribeOrderableDBInstanceOptionsPaginator(rds.NewFromConfig(s.cfg), &rds.DescribeOrderableDBInstanceOptionsInput{Engine: sdk.String(engine), Vpc: sdk.Bool(true)})
	for pages.HasMorePages() {
		page, err := pages.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, o := range page.OrderableDBInstanceOptions {
			version := sdk.ToString(o.EngineVersion)
			if sdk.ToString(o.StorageType) == "gp3" && (major == "" || major == version || strings.HasPrefix(version, major+".")) {
				classes[sdk.ToString(o.DBInstanceClass)] = true
			}
		}
	}
	return slices.Sorted(maps.Keys(classes)), nil
}

func (s *Session) RedisScaleSizes(ctx context.Context, id string) ([]string, error) {
	out, err := elasticache.NewFromConfig(s.cfg).ListAllowedNodeTypeModifications(ctx, &elasticache.ListAllowedNodeTypeModificationsInput{ReplicationGroupId: sdk.String(id)})
	if err != nil {
		return nil, err
	}
	return append(out.ScaleUpModifications, out.ScaleDownModifications...), nil
}
