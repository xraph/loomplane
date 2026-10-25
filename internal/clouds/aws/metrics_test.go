package aws

import (
	"context"
	"errors"
	"testing"
	"time"

	sdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	cwtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"
	"github.com/aws/aws-sdk-go-v2/service/costexplorer"
	cetypes "github.com/aws/aws-sdk-go-v2/service/costexplorer/types"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	elb "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	elbtypes "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2/types"
	"github.com/aws/smithy-go"

	"github.com/xraph/loomplane/internal/clouds/metrics"
)

const (
	testTargetGroupARN  = "arn:aws:elasticloadbalancing:us-east-1:123456789012:targetgroup/web-tg/6d0ecf831eec9f09"
	testLoadBalancerARN = "arn:aws:elasticloadbalancing:us-east-1:123456789012:loadbalancer/app/loomplane/50dc6c495c0c9188"
)

// fakeServices answers DescribeServices with one service.
type fakeServices struct {
	out *ecs.DescribeServicesOutput
	got *ecs.DescribeServicesInput
}

func (f *fakeServices) DescribeServices(_ context.Context, in *ecs.DescribeServicesInput, _ ...func(*ecs.Options)) (*ecs.DescribeServicesOutput, error) {
	f.got = in
	return f.out, nil
}

// fakeTargetGroups answers DescribeTargetGroups.
type fakeTargetGroups struct {
	out *elb.DescribeTargetGroupsOutput
	got *elb.DescribeTargetGroupsInput
}

func (f *fakeTargetGroups) DescribeTargetGroups(_ context.Context, in *elb.DescribeTargetGroupsInput, _ ...func(*elb.Options)) (*elb.DescribeTargetGroupsOutput, error) {
	f.got = in
	return f.out, nil
}

// fakeMetrics answers GetMetricData with pages, keyed by the token asked
// for, or with err.
type fakeMetrics struct {
	pages map[string]*cloudwatch.GetMetricDataOutput
	err   error
	got   []*cloudwatch.GetMetricDataInput
}

func (f *fakeMetrics) GetMetricData(_ context.Context, in *cloudwatch.GetMetricDataInput, _ ...func(*cloudwatch.Options)) (*cloudwatch.GetMetricDataOutput, error) {
	f.got = append(f.got, in)
	if f.err != nil {
		return nil, f.err
	}
	return f.pages[sdk.ToString(in.NextToken)], nil
}

func activeService(targetGroups ...string) *fakeServices {
	lbs := make([]ecstypes.LoadBalancer, 0, len(targetGroups))
	for _, tg := range targetGroups {
		lbs = append(lbs, ecstypes.LoadBalancer{TargetGroupArn: sdk.String(tg)})
	}
	return &fakeServices{out: &ecs.DescribeServicesOutput{Services: []ecstypes.Service{{
		Status: sdk.String("ACTIVE"), LoadBalancers: lbs,
	}}}}
}

func behindLoadBalancer() *fakeTargetGroups {
	return &fakeTargetGroups{out: &elb.DescribeTargetGroupsOutput{TargetGroups: []elbtypes.TargetGroup{{
		TargetGroupArn: sdk.String(testTargetGroupARN), LoadBalancerArns: []string{testLoadBalancerARN},
	}}}}
}

func testWindow() metrics.Window {
	start := time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)
	return metrics.Window{Start: start, End: start.Add(7 * 24 * time.Hour), Step: 3 * time.Hour}
}

func TestDimensionsAreARNSuffixes(t *testing.T) {
	dims, err := dimensions(testTargetGroupARN, testLoadBalancerARN)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"TargetGroup":  "targetgroup/web-tg/6d0ecf831eec9f09",
		"LoadBalancer": "app/loomplane/50dc6c495c0c9188",
	}
	if len(dims) != len(want) {
		t.Fatalf("dimensions = %+v", dims)
	}
	for _, d := range dims {
		if want[sdk.ToString(d.Name)] != sdk.ToString(d.Value) {
			t.Errorf("%s = %q, want %q", sdk.ToString(d.Name), sdk.ToString(d.Value), want[sdk.ToString(d.Name)])
		}
	}
	for _, bad := range [][2]string{
		{"not-an-arn", testLoadBalancerARN},
		{testTargetGroupARN, "arn:aws:elasticloadbalancing:us-east-1:123456789012:listener/app/x/y/z"},
	} {
		if _, err := dimensions(bad[0], bad[1]); err == nil {
			t.Errorf("dimensions(%q, %q) took an ARN it can't use", bad[0], bad[1])
		}
	}
}

func TestServiceMetricsReadsRequestsAndLatency(t *testing.T) {
	w := testWindow()
	away := time.FixedZone("away", 2*3600)
	first, second := w.Start.In(away), w.Start.Add(w.Step).In(away)
	cw := &fakeMetrics{pages: map[string]*cloudwatch.GetMetricDataOutput{
		"": {NextToken: sdk.String("page-2"), MetricDataResults: []cwtypes.MetricDataResult{
			{Id: sdk.String("requests"), Timestamps: []time.Time{first}, Values: []float64{12}},
		}},
		"page-2": {MetricDataResults: []cwtypes.MetricDataResult{
			{Id: sdk.String("requests"), Timestamps: []time.Time{second}, Values: []float64{30}},
			{Id: sdk.String("latency"), Timestamps: []time.Time{w.Start}, Values: []float64{0.25}},
		}},
	}}
	svc, tgs := activeService("", testTargetGroupARN), behindLoadBalancer()

	got, err := serviceMetrics(context.Background(), svc, tgs, cw, "loomplane", "web", w)
	if err != nil {
		t.Fatal(err)
	}

	if sdk.ToString(svc.got.Cluster) != "loomplane" || len(svc.got.Services) != 1 || svc.got.Services[0] != "web" {
		t.Errorf("described %+v", svc.got)
	}
	if len(tgs.got.TargetGroupArns) != 1 || tgs.got.TargetGroupArns[0] != testTargetGroupARN {
		t.Errorf("described target groups %v", tgs.got.TargetGroupArns)
	}
	want := []metrics.Point{{T: w.Start, V: 12}, {T: w.Start.Add(w.Step), V: 30}}
	if len(got.Requests) != len(want) {
		t.Fatalf("requests = %+v", got.Requests)
	}
	for i, p := range got.Requests {
		if !p.T.Equal(want[i].T) || p.T.Location() != time.UTC || p.V != want[i].V {
			t.Errorf("request %d = %v %v, want %v in UTC", i, p.T, p.V, want[i])
		}
	}
	if got.Latency != 250 || got.Statistic != metrics.P95 {
		t.Errorf("latency = %v %q, want 250 ms p95", got.Latency, got.Statistic)
	}

	if len(cw.got) != 2 || sdk.ToString(cw.got[1].NextToken) != "page-2" {
		t.Fatalf("didn't follow the next token: %+v", cw.got)
	}
	in := cw.got[0]
	if !sdk.ToTime(in.StartTime).Equal(w.Start) || !sdk.ToTime(in.EndTime).Equal(w.End) || in.ScanBy != cwtypes.ScanByTimestampAscending {
		t.Errorf("asked for %v to %v by %q", sdk.ToTime(in.StartTime), sdk.ToTime(in.EndTime), in.ScanBy)
	}
	queries := map[string]cwtypes.MetricDataQuery{}
	for _, q := range in.MetricDataQueries {
		queries[sdk.ToString(q.Id)] = q
	}
	for id, want := range map[string]struct {
		metric, stat string
		period       int32
	}{
		"requests": {"RequestCount", "Sum", int32(w.Step / time.Second)},
		"latency":  {"TargetResponseTime", "p95", int32(w.End.Sub(w.Start) / time.Second)},
	} {
		q, ok := queries[id]
		if !ok || q.MetricStat == nil || q.MetricStat.Metric == nil {
			t.Errorf("no %s query in %+v", id, in.MetricDataQueries)
			continue
		}
		m := q.MetricStat.Metric
		if sdk.ToString(m.Namespace) != "AWS/ApplicationELB" || sdk.ToString(m.MetricName) != want.metric ||
			sdk.ToString(q.MetricStat.Stat) != want.stat || sdk.ToInt32(q.MetricStat.Period) != want.period {
			t.Errorf("%s query = %s %s %s every %d", id, sdk.ToString(m.Namespace), sdk.ToString(m.MetricName),
				sdk.ToString(q.MetricStat.Stat), sdk.ToInt32(q.MetricStat.Period))
		}
		dims := map[string]string{}
		for _, d := range m.Dimensions {
			dims[sdk.ToString(d.Name)] = sdk.ToString(d.Value)
		}
		if dims["TargetGroup"] != "targetgroup/web-tg/6d0ecf831eec9f09" || dims["LoadBalancer"] != "app/loomplane/50dc6c495c0c9188" || len(dims) != 2 {
			t.Errorf("%s dimensions = %v", id, dims)
		}
	}
}

func TestServiceMetricsWithoutLatencyHasNoStatistic(t *testing.T) {
	cw := &fakeMetrics{pages: map[string]*cloudwatch.GetMetricDataOutput{"": {MetricDataResults: []cwtypes.MetricDataResult{
		{Id: sdk.String("requests")}, {Id: sdk.String("latency")},
	}}}}
	got, err := serviceMetrics(context.Background(), activeService(testTargetGroupARN), behindLoadBalancer(), cw, "c", "web", testWindow())
	if err != nil {
		t.Fatal(err)
	}
	if got.Latency != 0 || got.Statistic != "" || len(got.Requests) != 0 {
		t.Errorf("idle service = %+v", got)
	}
}

func TestServiceMetricsWithoutTrafficIsUnavailable(t *testing.T) {
	for name, svc := range map[string]*fakeServices{
		"no load balancer": activeService(),
		"gone":             {out: &ecs.DescribeServicesOutput{}},
		"draining":         {out: &ecs.DescribeServicesOutput{Services: []ecstypes.Service{{Status: sdk.String("DRAINING"), LoadBalancers: []ecstypes.LoadBalancer{{TargetGroupArn: sdk.String(testTargetGroupARN)}}}}}},
	} {
		t.Run(name, func(t *testing.T) {
			cw := &fakeMetrics{}
			_, err := serviceMetrics(context.Background(), svc, behindLoadBalancer(), cw, "c", "web", testWindow())
			reason, ok := metrics.IsUnavailable(err)
			if !ok || reason != "This service takes no traffic through a load balancer, so CloudWatch has nothing to report" {
				t.Errorf("err = %v", err)
			}
			if len(cw.got) != 0 {
				t.Errorf("asked CloudWatch anyway")
			}
		})
	}
}

func TestServiceMetricsAccessDeniedIsUnavailable(t *testing.T) {
	for _, code := range []string{"AccessDenied", "AccessDeniedException"} {
		cw := &fakeMetrics{err: &smithy.GenericAPIError{Code: code, Message: "not authorized to perform cloudwatch:GetMetricData"}}
		_, err := serviceMetrics(context.Background(), activeService(testTargetGroupARN), behindLoadBalancer(), cw, "c", "web", testWindow())
		reason, ok := metrics.IsUnavailable(err)
		if !ok || reason != "Allow cloudwatch:GetMetricData so Loomplane can read requests and latency" {
			t.Errorf("%s: err = %v", code, err)
		}
	}
}

func TestServiceMetricsOtherErrorsPassThrough(t *testing.T) {
	boom := errors.New("connection reset")
	cw := &fakeMetrics{err: boom}
	_, err := serviceMetrics(context.Background(), activeService(testTargetGroupARN), behindLoadBalancer(), cw, "c", "web", testWindow())
	if !errors.Is(err, boom) {
		t.Errorf("err = %v", err)
	}
	if _, ok := metrics.IsUnavailable(err); ok {
		t.Errorf("a failure to answer is not unavailable: %v", err)
	}
}

// fakeCosts answers GetCostAndUsage with pages, keyed by the token asked
// for, or with err.
type fakeCosts struct {
	pages map[string]*costexplorer.GetCostAndUsageOutput
	err   error
	got   []*costexplorer.GetCostAndUsageInput
}

func (f *fakeCosts) GetCostAndUsage(_ context.Context, in *costexplorer.GetCostAndUsageInput, _ ...func(*costexplorer.Options)) (*costexplorer.GetCostAndUsageOutput, error) {
	f.got = append(f.got, in)
	if f.err != nil {
		return nil, f.err
	}
	if out, ok := f.pages[sdk.ToString(in.NextPageToken)]; ok {
		return out, nil
	}
	return &costexplorer.GetCostAndUsageOutput{}, nil
}

func costDay(day, amount, unit string) cetypes.ResultByTime {
	return cetypes.ResultByTime{
		TimePeriod: &cetypes.DateInterval{Start: sdk.String(day)},
		Total:      map[string]cetypes.MetricValue{"UnblendedCost": {Amount: sdk.String(amount), Unit: sdk.String(unit)}},
	}
}

func TestSpendSumsDays(t *testing.T) {
	ce := &fakeCosts{pages: map[string]*costexplorer.GetCostAndUsageOutput{
		"": {NextPageToken: sdk.String("more"), ResultsByTime: []cetypes.ResultByTime{
			costDay("2026-10-01", "1.25", "USD"),
			costDay("2026-10-02", "0", "USD"),
		}},
		"more": {ResultsByTime: []cetypes.ResultByTime{
			costDay("2026-10-03", "2.5", "USD"),
		}},
	}}
	got, err := spend(context.Background(), ce, time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if got.Currency != "USD" || got.Total != 3.75 {
		t.Errorf("spend = %v %s, want 3.75 USD", got.Total, got.Currency)
	}
	want := []metrics.Point{
		{T: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), V: 1.25},
		{T: time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC), V: 2.5},
	}
	if len(got.Days) != len(want) {
		t.Fatalf("days = %+v", got.Days)
	}
	for i, d := range got.Days {
		if !d.T.Equal(want[i].T) || d.T.Location() != time.UTC || d.V != want[i].V {
			t.Errorf("day %d = %v, want %v", i, d, want[i])
		}
	}
	if len(ce.got) != 2 || sdk.ToString(ce.got[1].NextPageToken) != "more" {
		t.Fatalf("didn't follow the next page token: %+v", ce.got)
	}
	in := ce.got[0]
	if in.Granularity != cetypes.GranularityDaily || len(in.Metrics) != 1 || in.Metrics[0] != "UnblendedCost" {
		t.Errorf("asked for %q %v", in.Granularity, in.Metrics)
	}
}

func TestSpendAsksForThisMonthSoFar(t *testing.T) {
	for _, c := range []struct {
		name       string
		now        time.Time
		start, end string
	}{
		{"mid month", time.Date(2026, 10, 24, 15, 0, 0, 0, time.UTC), "2026-10-01", "2026-10-25"},
		{"first of the month", time.Date(2026, 10, 1, 0, 30, 0, 0, time.UTC), "2026-10-01", "2026-10-02"},
		{"last of the month", time.Date(2026, 10, 31, 23, 59, 0, 0, time.UTC), "2026-10-01", "2026-11-01"},
		{"last of the year", time.Date(2026, 12, 31, 8, 0, 0, 0, time.UTC), "2026-12-01", "2027-01-01"},
		{"ahead of UTC", time.Date(2026, 11, 1, 1, 0, 0, 0, time.FixedZone("ahead", 2*3600)), "2026-10-01", "2026-11-01"},
	} {
		t.Run(c.name, func(t *testing.T) {
			ce := &fakeCosts{}
			if _, err := spend(context.Background(), ce, c.now); err != nil {
				t.Fatal(err)
			}
			p := ce.got[0].TimePeriod
			if sdk.ToString(p.Start) != c.start || sdk.ToString(p.End) != c.end {
				t.Errorf("asked for %s to %s, want %s to %s", sdk.ToString(p.Start), sdk.ToString(p.End), c.start, c.end)
			}
		})
	}
}

func TestSpendAsksUSEast1(t *testing.T) {
	s := New("").Session(&Key{AccessKeyID: "AKIATESTTESTTESTTEST", SecretAccessKey: "secret-test-secret-test"}, "eu-west-2", "account")
	if got := costExplorer(s.cfg).Options().Region; got != "us-east-1" {
		t.Errorf("cost explorer region = %q", got)
	}
	if s.cfg.Region != "eu-west-2" {
		t.Errorf("the session's region moved to %q", s.cfg.Region)
	}
}

func TestSpendRefusesMixedCurrencies(t *testing.T) {
	ce := &fakeCosts{pages: map[string]*costexplorer.GetCostAndUsageOutput{"": {ResultsByTime: []cetypes.ResultByTime{
		costDay("2026-10-01", "1", "USD"),
		costDay("2026-10-02", "1", "EUR"),
	}}}}
	if got, err := spend(context.Background(), ce, time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)); err == nil {
		t.Errorf("mixed currencies summed to %+v", got)
	}
}

func TestSpendUnavailable(t *testing.T) {
	for name, c := range map[string]struct {
		err    error
		reason string
	}{
		"access denied": {&smithy.GenericAPIError{Code: "AccessDeniedException", Message: "not authorized to perform ce:GetCostAndUsage"},
			"Allow ce:GetCostAndUsage so Loomplane can read what the account costs"},
		"not enabled": {&smithy.GenericAPIError{Code: "ValidationException", Message: "User not enabled for cost explorer access"},
			"Turn on Cost Explorer in the AWS Billing console; it takes up to a day to start reporting"},
		"no data yet": {&cetypes.DataUnavailableException{Message: sdk.String("Data is not available")},
			"Turn on Cost Explorer in the AWS Billing console; it takes up to a day to start reporting"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := spend(context.Background(), &fakeCosts{err: c.err}, time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC))
			if reason, ok := metrics.IsUnavailable(err); !ok || reason != c.reason {
				t.Errorf("err = %v", err)
			}
		})
	}
}
