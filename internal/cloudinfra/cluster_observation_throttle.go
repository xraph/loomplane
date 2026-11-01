package cloudinfra

import (
	"context"
	"errors"
	"time"

	"github.com/xraph/loomplane/pkg/provider"
)

type clusterObservationDelay interface {
	ScalingObservationDeadline(context.Context, string, string, string, string) (time.Time, error)
	DelayScalingObservation(context.Context, string, string, string, string, int64, time.Duration) error
}

func (s *PgStore) ScalingObservationDeadline(ctx context.Context, org, cloud, resource, pool string) (time.Time, error) {
	var due *time.Time
	err := s.db.QueryRow(ctx, `SELECT (SELECT retry_observation_at FROM loomplane_cluster_pool_advice WHERE org_id=$1 AND cloud_id=$2 AND resource_id=$3 AND pool_id=$4)`, org, cloud, resource, pool).Scan(&due)
	if due == nil {
		return time.Time{}, err
	}
	return *due, err
}

func (s *PgStore) DelayScalingObservation(ctx context.Context, org, cloud, resource, pool string, revision int64, delay time.Duration) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollback(tx)
	if checkErr := lockCapacityCloud(ctx, tx, org, cloud); checkErr != nil {
		return checkErr
	}
	if checkErr := scalingObserverTx(ctx, tx, org, cloud, resource, pool, revision); checkErr != nil {
		return checkErr
	}
	_, err = tx.Exec(ctx, `INSERT INTO loomplane_cluster_pool_advice AS prev(org_id,cloud_id,resource_id,pool_id,policy_revision,observed_at,retry_observation_at) VALUES($1,$2,$3,$4,$5,clock_timestamp(),clock_timestamp()+make_interval(secs=>$6)) ON CONFLICT(org_id,cloud_id,resource_id,pool_id) DO UPDATE SET low_since=NULL,review_decrease=false,retry_observation_at=greatest(prev.retry_observation_at,excluded.retry_observation_at)`, org, cloud, resource, pool, revision, delay.Seconds())
	if err != nil {
		return err
	}
	return tx.Commit()
}

func providerObservationDelay(err error) time.Duration {
	var api *provider.InfrastructureAPIError
	if !errors.As(err, &api) || api.Status != 429 {
		return 0
	}
	return max(api.RetryAfter, time.Minute)
}
