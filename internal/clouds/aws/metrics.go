package aws

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	sdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	cwtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"
	"github.com/aws/aws-sdk-go-v2/service/costexplorer"
	cetypes "github.com/aws/aws-sdk-go-v2/service/costexplorer/types"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	elb "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	"github.com/aws/smithy-go"

	"github.com/xraph/loomplane/internal/clouds/metrics"
)

// What AWS can't report, and why.
const (
	noTraffic     = "This service takes no traffic through a load balancer, so CloudWatch has nothing to report"
	noMetricsRead = "Allow cloudwatch:GetMetricData so Loomplane can read requests and latency"
	noCostRead    = "Allow ce:GetCostAndUsage so Loomplane can read what the account costs"
	noCostReports = "Turn on Cost Explorer in the AWS Billing console; it takes up to a day to start reporting"
)

// serviceDescriber is the part of ECS that finds a service's target group.
type serviceDescriber interface {
	DescribeServices(context.Context, *ecs.DescribeServicesInput, ...func(*ecs.Options)) (*ecs.DescribeServicesOutput, error)
}

// targetGroupDescriber is the part of ELB that finds a target group's
// load balancer.
type targetGroupDescriber interface {
	DescribeTargetGroups(context.Context, *elb.DescribeTargetGroupsInput, ...func(*elb.Options)) (*elb.DescribeTargetGroupsOutput, error)
}

// metricReader is the part of CloudWatch that reads metrics.
type metricReader interface {
	GetMetricData(context.Context, *cloudwatch.GetMetricDataInput, ...func(*cloudwatch.Options)) (*cloudwatch.GetMetricDataOutput, error)
}

// costReader is the part of Cost Explorer that reads what an account
// costs.
type costReader interface {
	GetCostAndUsage(context.Context, *costexplorer.GetCostAndUsageInput, ...func(*costexplorer.Options)) (*costexplorer.GetCostAndUsageOutput, error)
}

// ServiceMetrics reads an ECS service's requests, p95 latency and 5xx counts over w
// from CloudWatch, through the target group it runs behind.
func (s *Session) ServiceMetrics(ctx context.Context, cluster, service string, w metrics.Window) (*metrics.Measurement, error) {
	return serviceMetrics(ctx, ecs.NewFromConfig(s.cfg), elb.NewFromConfig(s.cfg), cloudwatch.NewFromConfig(s.cfg), cluster, service, w)
}

func serviceMetrics(ctx context.Context, services serviceDescriber, groups targetGroupDescriber, cw metricReader,
	cluster, service string, w metrics.Window) (*metrics.Measurement, error) {
	got, err := services.DescribeServices(ctx, &ecs.DescribeServicesInput{Cluster: sdk.String(cluster), Services: []string{service}})
	if err != nil {
		return nil, fmt.Errorf("read service %s: %w", service, err)
	}
	if len(got.Services) == 0 || sdk.ToString(got.Services[0].Status) != "ACTIVE" {
		return nil, metrics.Unavailablef(noTraffic)
	}
	var targetGroup string
	for _, lb := range got.Services[0].LoadBalancers {
		if arn := sdk.ToString(lb.TargetGroupArn); arn != "" {
			targetGroup = arn
			break
		}
	}
	if targetGroup == "" {
		return nil, metrics.Unavailablef(noTraffic)
	}
	return targetGroupMetrics(ctx, groups, cw, targetGroup, w)
}

// TargetGroupMetrics reads one ALB target group's requests, p95 latency and
// 5xx counts over w. A canary reads its candidate's group this way.
func (s *Session) TargetGroupMetrics(ctx context.Context, targetGroupARN string, w metrics.Window) (*metrics.Measurement, error) {
	return targetGroupMetrics(ctx, elb.NewFromConfig(s.cfg), cloudwatch.NewFromConfig(s.cfg), targetGroupARN, w)
}

func targetGroupMetrics(ctx context.Context, groups targetGroupDescriber, cw metricReader,
	targetGroup string, w metrics.Window) (*metrics.Measurement, error) {
	tgs, err := groups.DescribeTargetGroups(ctx, &elb.DescribeTargetGroupsInput{TargetGroupArns: []string{targetGroup}})
	if err != nil {
		return nil, fmt.Errorf("read target group %s: %w", targetGroup, err)
	}
	if len(tgs.TargetGroups) == 0 || len(tgs.TargetGroups[0].LoadBalancerArns) == 0 {
		return nil, metrics.Unavailablef(noTraffic)
	}
	dims, err := dimensions(targetGroup, tgs.TargetGroups[0].LoadBalancerArns[0])
	if err != nil {
		return nil, err
	}

	query := func(id, metric, stat string, period time.Duration) cwtypes.MetricDataQuery {
		return cwtypes.MetricDataQuery{Id: sdk.String(id), MetricStat: &cwtypes.MetricStat{
			Metric: &cwtypes.Metric{Namespace: sdk.String("AWS/ApplicationELB"), MetricName: sdk.String(metric), Dimensions: dims},
			Stat:   sdk.String(stat), Period: sdk.Int32(int32(period / time.Second)), //nolint:gosec // G115: a window of at most 90 days, in seconds
		}}
	}
	in := &cloudwatch.GetMetricDataInput{
		StartTime: sdk.Time(w.Start), EndTime: sdk.Time(w.End), ScanBy: cwtypes.ScanByTimestampAscending,
		MetricDataQueries: []cwtypes.MetricDataQuery{
			query("requests", "RequestCount", "Sum", w.Step),
			query("latency", "TargetResponseTime", "p95", w.End.Sub(w.Start)),
			query("errors", "HTTPCode_Target_5XX_Count", "Sum", w.Step),
		},
	}
	m := &metrics.Measurement{ErrorsCounted: true}
	for {
		out, err := cw.GetMetricData(ctx, in)
		if err != nil {
			if denied(err) {
				return nil, metrics.Unavailablef(noMetricsRead)
			}
			return nil, fmt.Errorf("read metrics: %w", err)
		}
		for _, r := range out.MetricDataResults {
			switch sdk.ToString(r.Id) {
			case "requests":
				for i, t := range r.Timestamps {
					if i < len(r.Values) {
						m.Requests = append(m.Requests, metrics.Point{T: t.UTC(), V: r.Values[i]})
					}
				}
			case "errors":
				for i, t := range r.Timestamps {
					if i < len(r.Values) {
						m.Errors = append(m.Errors, metrics.Point{T: t.UTC(), V: r.Values[i]})
					}
				}
			case "latency":
				if len(r.Values) > 0 && m.Statistic == "" {
					m.Latency, m.Statistic = r.Values[0]*1000, metrics.P95
				}
			}
		}
		if sdk.ToString(out.NextToken) == "" {
			return m, nil
		}
		in.NextToken = out.NextToken
	}
}

// Spend reads what the account has cost this month from Cost Explorer.
func (s *Session) Spend(ctx context.Context, now time.Time) (*metrics.Spend, error) {
	return spend(ctx, costExplorer(s.cfg), now)
}

// costExplorer is a Cost Explorer client. It only answers in us-east-1,
// whatever region the session is in.
func costExplorer(cfg sdk.Config) *costexplorer.Client {
	cfg = cfg.Copy()
	cfg.Region = "us-east-1"
	return costexplorer.NewFromConfig(cfg)
}

func spend(ctx context.Context, ce costReader, now time.Time) (*metrics.Spend, error) {
	start, end := metrics.CalendarMonth(now)
	now = now.UTC()
	if tomorrow := time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, time.UTC); tomorrow.Before(end) {
		end = tomorrow
	}
	in := &costexplorer.GetCostAndUsageInput{
		TimePeriod:  &cetypes.DateInterval{Start: sdk.String(start.Format(time.DateOnly)), End: sdk.String(end.Format(time.DateOnly))},
		Granularity: cetypes.GranularityDaily,
		Metrics:     []string{"UnblendedCost"},
	}
	out := &metrics.Spend{}
	var unit string
	for {
		page, err := ce.GetCostAndUsage(ctx, in)
		if err != nil {
			switch {
			case costsOff(err):
				return nil, metrics.Unavailablef(noCostReports)
			case denied(err):
				return nil, metrics.Unavailablef(noCostRead)
			}
			return nil, fmt.Errorf("read costs: %w", err)
		}
		for _, r := range page.ResultsByTime {
			cost, ok := r.Total["UnblendedCost"]
			if !ok || r.TimePeriod == nil {
				continue
			}
			amount, err := strconv.ParseFloat(sdk.ToString(cost.Amount), 64)
			if err != nil {
				return nil, fmt.Errorf("read costs: %q isn't an amount", sdk.ToString(cost.Amount))
			}
			if unit == "" {
				unit = sdk.ToString(cost.Unit)
			}
			if amount == 0 {
				continue
			}
			if out.Currency == "" {
				out.Currency = sdk.ToString(cost.Unit)
			} else if c := sdk.ToString(cost.Unit); c != out.Currency {
				return nil, fmt.Errorf("read costs: the account is billed in both %s and %s", out.Currency, c)
			}
			day, err := time.Parse(time.DateOnly, sdk.ToString(r.TimePeriod.Start))
			if err != nil {
				return nil, fmt.Errorf("read costs: %q isn't a day", sdk.ToString(r.TimePeriod.Start))
			}
			out.Days = append(out.Days, metrics.Point{T: day, V: amount})
			out.Total += amount
		}
		if sdk.ToString(page.NextPageToken) == "" {
			break
		}
		in.NextPageToken = page.NextPageToken
	}
	if out.Currency == "" {
		out.Currency = unit
	}
	return out, nil
}

// costsOff reports whether err is Cost Explorer saying it isn't turned on
// for the account, or has nothing to report yet.
func costsOff(err error) bool {
	var unavailable *cetypes.DataUnavailableException
	if errors.As(err, &unavailable) {
		return true
	}
	var ae smithy.APIError
	if errors.As(err, &ae) && ae.ErrorCode() == "DataUnavailableException" {
		return true
	}
	return strings.Contains(strings.ToLower(err.Error()), "not enabled for cost explorer")
}

// dimensions are the CloudWatch dimensions for a target group behind a
// load balancer: the ends of their ARNs, from "targetgroup/" and after
// "loadbalancer/".
func dimensions(targetGroupARN, loadBalancerARN string) ([]cwtypes.Dimension, error) {
	i := strings.Index(targetGroupARN, ":targetgroup/")
	if i < 0 {
		return nil, fmt.Errorf("%q isn't a target group ARN", targetGroupARN)
	}
	_, lb, ok := strings.Cut(loadBalancerARN, ":loadbalancer/")
	if !ok || lb == "" {
		return nil, fmt.Errorf("%q isn't a load balancer ARN", loadBalancerARN)
	}
	return []cwtypes.Dimension{
		{Name: sdk.String("TargetGroup"), Value: sdk.String(targetGroupARN[i+1:])},
		{Name: sdk.String("LoadBalancer"), Value: sdk.String(lb)},
	}, nil
}

// denied reports whether AWS refused the request for want of a
// permission.
func denied(err error) bool {
	var ae smithy.APIError
	if !errors.As(err, &ae) {
		return false
	}
	code := ae.ErrorCode()
	return code == "AccessDenied" || code == "AccessDeniedException"
}
