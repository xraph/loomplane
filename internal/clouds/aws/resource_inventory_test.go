package aws

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestResourceInventoryNativeProtocolsAndScope(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		switch r.Form.Get("Action") {
		case "DescribeDBInstances":
			w.Header().Set("Content-Type", "text/xml")
			w.Write([]byte(`<DescribeDBInstancesResponse><DescribeDBInstancesResult><DBInstances><DBInstance><DBInstanceArn>arn:aws:rds:us-east-1:123456789012:db:main</DBInstanceArn><DBInstanceIdentifier>main</DBInstanceIdentifier><DbiResourceId>db-physical</DbiResourceId><Engine>postgres</Engine><EngineVersion>16</EngineVersion></DBInstance></DBInstances></DescribeDBInstancesResult></DescribeDBInstancesResponse>`))
		case "DescribeReplicationGroups":
			w.Write([]byte(`<DescribeReplicationGroupsResponse><DescribeReplicationGroupsResult><ReplicationGroups><ReplicationGroup><ARN>arn:aws:elasticache:us-east-1:123456789012:replicationgroup:cache</ARN><ReplicationGroupId>cache</ReplicationGroupId></ReplicationGroup></ReplicationGroups></DescribeReplicationGroupsResult></DescribeReplicationGroupsResponse>`))
		default:
			if r.Method != "GET" || r.URL.Query().Get("bucket-region") != "us-east-1" {
				t.Errorf("unexpected %s %s", r.Method, r.URL)
			}
			w.Write([]byte(`<ListAllMyBucketsResult><Buckets><Bucket><Name>bucket</Name><CreationDate>2026-01-01T00:00:00Z</CreationDate><BucketRegion>us-east-1</BucketRegion></Bucket></Buckets></ListAllMyBucketsResult>`))
		}
	}))
	defer srv.Close()
	s := New(srv.URL).Session(&Key{AccessKeyID: "AKIATEST", SecretAccessKey: "secret"}, "us-east-1", "123456789012")
	got, err := s.ResourceInventory(t.Context())
	if err != nil || !got.Complete || len(got.Resources) != 3 || !strings.Contains(got.Scope, "us-east-1") {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestResourceInventoryBucketsDoNotClaimMutableCreationDateIsIncarnation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		switch r.Form.Get("Action") {
		case "DescribeDBInstances":
			w.Write([]byte(`<DescribeDBInstancesResponse><DescribeDBInstancesResult/></DescribeDBInstancesResponse>`))
		case "DescribeReplicationGroups":
			w.Write([]byte(`<DescribeReplicationGroupsResponse><DescribeReplicationGroupsResult/></DescribeReplicationGroupsResponse>`))
		default:
			w.Write([]byte(`<ListAllMyBucketsResult><Buckets><Bucket><Name>bucket</Name><CreationDate>2026-01-01T00:00:00Z</CreationDate><BucketRegion>us-east-1</BucketRegion></Bucket></Buckets></ListAllMyBucketsResult>`))
		}
	}))
	defer srv.Close()
	got, err := New(srv.URL).Session(&Key{AccessKeyID: "AKIATEST", SecretAccessKey: "secret"}, "us-east-1", "123456789012").ResourceInventory(t.Context())
	if err != nil || !got.Complete || len(got.Resources) != 1 || got.Resources[0].Incarnation != "" {
		t.Fatalf("bucket incarnation is unproven: %+v %v", got, err)
	}
}

func TestResourceInventoryPaginationAndFailureNeverComplete(t *testing.T) {
	for _, scenario := range []string{"pages", "repeated-rds", "repeated-cache", "repeated-buckets", "foreign-account", "foreign-region", "duplicate", "denied"} {
		t.Run(scenario, func(t *testing.T) {
			counts := map[string]int{}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				r.ParseForm()
				action := r.Form.Get("Action")
				counts[action]++
				if scenario == "denied" {
					w.WriteHeader(403)
					w.Write([]byte(`SECRET`))
					return
				}
				account, region := "123456789012", "us-east-1"
				if scenario == "foreign-account" {
					account = "999999999999"
				}
				if scenario == "foreign-region" {
					region = "us-west-2"
				}
				switch action {
				case "DescribeDBInstances":
					marker := ""
					if counts[action] == 1 || scenario == "repeated-rds" {
						marker = "<Marker>next</Marker>"
					}
					items := ""
					if counts[action] == 1 || scenario == "duplicate" {
						items = fmt.Sprintf(`<DBInstance><DBInstanceArn>arn:aws:rds:%s:%s:db:main</DBInstanceArn><DBInstanceIdentifier>main</DBInstanceIdentifier><DbiResourceId>physical</DbiResourceId><Engine>mysql</Engine></DBInstance>`, region, account)
					}
					if counts[action] > 1 && r.Form.Get("Marker") != "next" {
						t.Error("missing RDS marker")
					}
					fmt.Fprintf(w, `<DescribeDBInstancesResponse><DescribeDBInstancesResult><DBInstances>%s</DBInstances>%s</DescribeDBInstancesResult></DescribeDBInstancesResponse>`, items, marker)
				case "DescribeReplicationGroups":
					marker := ""
					if counts[action] == 1 || scenario == "repeated-cache" {
						marker = "<Marker>next</Marker>"
					}
					if counts[action] > 1 && r.Form.Get("Marker") != "next" {
						t.Error("missing ElastiCache marker")
					}
					fmt.Fprintf(w, `<DescribeReplicationGroupsResponse><DescribeReplicationGroupsResult><ReplicationGroups/>%s</DescribeReplicationGroupsResult></DescribeReplicationGroupsResponse>`, marker)
				default:
					if r.Method != "GET" || r.URL.Path != "/" || r.URL.Query().Get("bucket-region") != "us-east-1" {
						t.Errorf("unexpected request %s %s", r.Method, r.URL)
					}
					token := ""
					if counts[action] == 1 || scenario == "repeated-buckets" {
						token = "<ContinuationToken>next</ContinuationToken>"
					}
					if counts[action] > 1 && r.URL.Query().Get("continuation-token") != "next" {
						t.Error("missing S3 token")
					}
					fmt.Fprintf(w, `<ListAllMyBucketsResult><Buckets/>%s</ListAllMyBucketsResult>`, token)
				}
			}))
			defer srv.Close()
			got, err := New(srv.URL).Session(&Key{AccessKeyID: "AKIATEST", SecretAccessKey: "secret"}, "us-east-1", "123456789012").ResourceInventory(t.Context())
			if scenario == "pages" {
				if err != nil || !got.Complete || len(got.Resources) != 1 || got.Resources[0].Kind != "mysql" || counts["DescribeDBInstances"] != 2 || counts["DescribeReplicationGroups"] != 2 || counts[""] != 2 {
					t.Fatalf("%+v %v counts=%v", got, err, counts)
				}
			} else if err == nil || got.Complete || strings.Contains(err.Error(), "SECRET") {
				t.Fatalf("%+v %v", got, err)
			}
		})
	}
}
