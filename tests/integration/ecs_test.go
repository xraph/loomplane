//go:build integration

package integration

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/config"
)

// fakeECS is the AWS behind an ECS deployment: what it was asked to
// create, and enough state to answer what Loomplane reads back.
type fakeECS struct {
	mu        sync.Mutex
	base      string
	repos     map[string]string
	builds    []map[string]any
	polls     map[string]int
	roles     map[string]bool
	groups    map[string]string
	lbs       map[string]bool
	tgs       map[string]string
	listeners []map[string]any
	tasks     []map[string]any
	services  map[string]map[string]any
	secrets   map[string]string
	buckets   map[string]bool
	uploads   map[string]int
	// ingress is each group's rules, as "from-group:port".
	ingress map[string][]string
	// dbs are RDS instances' create requests, by identifier, and
	// dbPolls how often each was read.
	dbs     map[string]url.Values
	dbPolls map[string]int
	// namespaces are Cloud Map namespaces, by name.
	namespaces map[string]string
	// caches are ElastiCache create requests, by ID, and cachePolls how
	// often each was read; deleted lists what was deleted.
	caches     map[string]url.Values
	cachePolls map[string]int
	deleted    []string
	// policies are each role's inline policy, private the buckets made
	// private.
	policies map[string]string
	private  map[string]bool
}

const ecsAPI = "AmazonEC2ContainerServiceV20141113."

func installFakeECS(t *testing.T, f *fakeAWS) *fakeECS {
	t.Helper()
	e := &fakeECS{
		base: f.srv.URL, repos: map[string]string{}, polls: map[string]int{}, roles: map[string]bool{},
		groups: map[string]string{}, lbs: map[string]bool{}, tgs: map[string]string{},
		services: map[string]map[string]any{}, secrets: map[string]string{}, buckets: map[string]bool{}, uploads: map[string]int{},
		ingress: map[string][]string{}, dbs: map[string]url.Values{}, dbPolls: map[string]int{},
		namespaces: map[string]string{}, caches: map[string]url.Values{}, cachePolls: map[string]int{},
		policies: map[string]string{}, private: map[string]bool{},
	}
	j := func(target string, h func(in map[string]any) (int, any)) {
		f.json[target] = func(body []byte) (int, string) {
			var in map[string]any
			_ = json.Unmarshal(body, &in)
			e.mu.Lock()
			defer e.mu.Unlock()
			status, out := h(in)
			data, _ := json.Marshal(out)
			return status, string(data)
		}
	}
	q := func(action string, h func(form url.Values) (int, string)) {
		f.query[action] = func(form url.Values) (int, string) {
			e.mu.Lock()
			defer e.mu.Unlock()
			return h(form)
		}
	}
	notFound := func(kind string) (int, any) {
		return 400, map[string]any{"__type": kind, "message": "not found"}
	}

	// ECR.
	j("AmazonEC2ContainerRegistry_V20150921.DescribeRepositories", func(in map[string]any) (int, any) {
		name := in["repositoryNames"].([]any)[0].(string)
		uri, ok := e.repos[name]
		if !ok {
			return notFound("RepositoryNotFoundException")
		}
		return 200, map[string]any{"repositories": []any{map[string]any{"repositoryName": name, "repositoryUri": uri}}}
	})
	j("AmazonEC2ContainerRegistry_V20150921.CreateRepository", func(in map[string]any) (int, any) {
		name := in["repositoryName"].(string)
		e.repos[name] = awsAccount + ".dkr.ecr.us-east-1.amazonaws.com/" + name
		return 200, map[string]any{"repository": map[string]any{"repositoryName": name, "repositoryUri": e.repos[name]}}
	})
	// CodeBuild.
	j("CodeBuild_20161006.CreateProject", func(in map[string]any) (int, any) {
		return 200, map[string]any{"project": map[string]any{"name": in["name"]}}
	})
	j("CodeBuild_20161006.UpdateProject", func(in map[string]any) (int, any) {
		return 200, map[string]any{"project": map[string]any{"name": in["name"]}}
	})
	j("CodeBuild_20161006.StartBuild", func(in map[string]any) (int, any) {
		e.builds = append(e.builds, in)
		return 200, map[string]any{"build": map[string]any{"id": fmt.Sprintf("loomplane:%d", len(e.builds))}}
	})
	j("CodeBuild_20161006.BatchGetBuilds", func(in map[string]any) (int, any) {
		id := in["ids"].([]any)[0].(string)
		e.polls[id]++
		status := "IN_PROGRESS"
		if e.polls[id] > 1 {
			status = "SUCCEEDED"
		}
		return 200, map[string]any{"builds": []any{map[string]any{"id": id, "buildStatus": status, "currentPhase": "BUILD", "logs": map[string]any{"groupName": "loomplane-build", "streamName": id}}}}
	})
	// Logs and Secrets Manager.
	j("Logs_20140328.CreateLogGroup", func(map[string]any) (int, any) { return 200, map[string]any{} })
	j("Logs_20140328.GetLogEvents", func(in map[string]any) (int, any) {
		if in["nextToken"] == "end" {
			return 200, map[string]any{"events": []any{}, "nextForwardToken": "end"}
		}
		return 200, map[string]any{"events": []any{map[string]any{"timestamp": 1000, "ingestionTime": 1001, "message": "CodeBuild fixture output"}}, "nextForwardToken": "end"}
	})
	j("secretsmanager.CreateSecret", func(in map[string]any) (int, any) {
		name := in["Name"].(string)
		e.secrets[name] = in["SecretString"].(string)
		return 200, map[string]any{"ARN": "arn:aws:secretsmanager:us-east-1:" + awsAccount + ":secret:" + name + "-AbCdEf", "Name": name}
	})
	// Cloud Map.
	const cloudMap = "Route53AutoNaming_v20170314."
	nsARN := func(id string) string { return "arn:aws:servicediscovery:us-east-1:" + awsAccount + ":namespace/" + id }
	j(cloudMap+"ListNamespaces", func(map[string]any) (int, any) {
		list := make([]any, 0, len(e.namespaces))
		for name, id := range e.namespaces {
			list = append(list, map[string]any{"Id": id, "Name": name, "Arn": nsARN(id), "Type": "HTTP"})
		}
		return 200, map[string]any{"Namespaces": list}
	})
	j(cloudMap+"CreateHttpNamespace", func(in map[string]any) (int, any) {
		id := fmt.Sprintf("ns-%d", len(e.namespaces)+1)
		e.namespaces[in["Name"].(string)] = id
		return 200, map[string]any{"OperationId": "op-" + id}
	})
	j(cloudMap+"GetOperation", func(in map[string]any) (int, any) {
		id := strings.TrimPrefix(in["OperationId"].(string), "op-")
		return 200, map[string]any{"Operation": map[string]any{"Id": in["OperationId"], "Status": "SUCCESS", "Targets": map[string]any{"NAMESPACE": id}}}
	})
	j(cloudMap+"GetNamespace", func(in map[string]any) (int, any) {
		id := in["Id"].(string)
		return 200, map[string]any{"Namespace": map[string]any{"Id": id, "Arn": nsARN(id), "Type": "HTTP"}}
	})
	// ECS.
	j(ecsAPI+"CreateCluster", func(in map[string]any) (int, any) {
		return 200, map[string]any{"cluster": map[string]any{"clusterName": in["clusterName"], "status": "ACTIVE"}}
	})
	j(ecsAPI+"RegisterTaskDefinition", func(in map[string]any) (int, any) {
		e.tasks = append(e.tasks, in)
		arn := fmt.Sprintf("arn:aws:ecs:us-east-1:%s:task-definition/%s:%d", awsAccount, in["family"], len(e.tasks))
		return 200, map[string]any{"taskDefinition": map[string]any{"taskDefinitionArn": arn, "containerDefinitions": in["containerDefinitions"]}}
	})
	j(ecsAPI+"DescribeServices", func(in map[string]any) (int, any) {
		name := in["services"].([]any)[0].(string)
		svc, ok := e.services[name]
		if !ok {
			return 200, map[string]any{"services": []any{}, "failures": []any{map[string]any{"reason": "MISSING"}}}
		}
		return 200, map[string]any{"services": []any{svc}}
	})
	upsert := func(name string, in map[string]any) map[string]any {
		svc := e.services[name]
		if svc == nil {
			svc = map[string]any{"serviceName": name, "status": "ACTIVE", "request": in}
		}
		svc["taskDefinition"] = in["taskDefinition"]
		svc["deployments"] = []any{map[string]any{"status": "PRIMARY", "rolloutState": "COMPLETED", "runningCount": 1, "desiredCount": 1}}
		e.services[name] = svc
		return svc
	}
	j(ecsAPI+"CreateService", func(in map[string]any) (int, any) {
		return 200, map[string]any{"service": upsert(in["serviceName"].(string), in)}
	})
	j(ecsAPI+"UpdateService", func(in map[string]any) (int, any) {
		return 200, map[string]any{"service": upsert(in["service"].(string), in)}
	})
	j(ecsAPI+"DeleteService", func(in map[string]any) (int, any) {
		name := in["service"].(string)
		svc, ok := e.services[name]
		if !ok {
			return 400, map[string]any{"__type": "ServiceNotFoundException", "message": "service not found"}
		}
		delete(e.services, name)
		e.deleted = append(e.deleted, name)
		svc["status"] = "DRAINING"
		return 200, map[string]any{"service": svc}
	})
	j(ecsAPI+"DescribeTaskDefinition", func(in map[string]any) (int, any) {
		for _, td := range e.tasks {
			if strings.Contains(in["taskDefinition"].(string), "/"+td["family"].(string)+":") {
				return 200, map[string]any{"taskDefinition": map[string]any{"containerDefinitions": td["containerDefinitions"]}}
			}
		}
		return notFound("ClientException")
	})

	// EC2, in its own query dialect.
	ec2 := func(action, inner string) string {
		return `<` + action + `Response xmlns="http://ec2.amazonaws.com/doc/2016-11-15/"><requestId>1</requestId>` + inner + `</` + action + `Response>`
	}
	q("DescribeVpcs", func(url.Values) (int, string) {
		return 200, ec2("DescribeVpcs", `<vpcSet><item><vpcId>vpc-1</vpcId><isDefault>true</isDefault></item></vpcSet>`)
	})
	q("DescribeSubnets", func(url.Values) (int, string) {
		return 200, ec2("DescribeSubnets", `<subnetSet><item><subnetId>subnet-a</subnetId></item><item><subnetId>subnet-b</subnetId></item></subnetSet>`)
	})
	q("DescribeSecurityGroups", func(form url.Values) (int, string) {
		name := ""
		for i := 1; i < 5; i++ {
			if form.Get(fmt.Sprintf("Filter.%d.Name", i)) == "group-name" {
				name = form.Get(fmt.Sprintf("Filter.%d.Value.1", i))
			}
		}
		if id, ok := e.groups[name]; ok {
			return 200, ec2("DescribeSecurityGroups", `<securityGroupInfo><item><groupId>`+id+`</groupId><groupName>`+name+`</groupName></item></securityGroupInfo>`)
		}
		return 200, ec2("DescribeSecurityGroups", `<securityGroupInfo/>`)
	})
	q("CreateSecurityGroup", func(form url.Values) (int, string) {
		id := fmt.Sprintf("sg-%d", len(e.groups)+1)
		e.groups[form.Get("GroupName")] = id
		return 200, ec2("CreateSecurityGroup", `<return>true</return><groupId>`+id+`</groupId>`)
	})
	q("AuthorizeSecurityGroupIngress", func(form url.Values) (int, string) {
		from := form.Get("IpPermissions.1.Groups.1.GroupId")
		if from == "" {
			from = form.Get("IpPermissions.1.IpRanges.1.CidrIp")
		}
		g := form.Get("GroupId")
		e.ingress[g] = append(e.ingress[g], from+":"+form.Get("IpPermissions.1.FromPort"))
		return 200, ec2("AuthorizeSecurityGroupIngress", `<return>true</return>`)
	})

	// IAM and load balancing, in the common query dialect.
	resp := func(action, ns, inner string) string {
		return `<` + action + `Response xmlns="` + ns + `"><` + action + `Result>` + inner + `</` + action + `Result><ResponseMetadata><RequestId>1</RequestId></ResponseMetadata></` + action + `Response>`
	}
	fail := func(code string) (int, string) {
		return 400, `<ErrorResponse><Error><Type>Sender</Type><Code>` + code + `</Code><Message>not found</Message></Error><RequestId>1</RequestId></ErrorResponse>`
	}
	const iamNS, elbNS = "https://iam.amazonaws.com/doc/2010-05-08/", "http://elasticloadbalancing.amazonaws.com/doc/2015-12-01/"
	role := func(name string) string {
		return `<Role><RoleName>` + name + `</RoleName><Arn>arn:aws:iam::` + awsAccount + `:role/` + name + `</Arn></Role>`
	}
	q("GetRole", func(form url.Values) (int, string) {
		if !e.roles[form.Get("RoleName")] {
			return fail("NoSuchEntity")
		}
		return 200, resp("GetRole", iamNS, role(form.Get("RoleName")))
	})
	q("CreateRole", func(form url.Values) (int, string) {
		e.roles[form.Get("RoleName")] = true
		return 200, resp("CreateRole", iamNS, role(form.Get("RoleName")))
	})
	q("AttachRolePolicy", func(url.Values) (int, string) { return 200, resp("AttachRolePolicy", iamNS, "") })
	q("PutRolePolicy", func(form url.Values) (int, string) {
		e.policies[form.Get("RoleName")] = form.Get("PolicyDocument")
		return 200, resp("PutRolePolicy", iamNS, "")
	})
	lbMember := func(name string) string {
		// The DNS name is the fake itself, so the health check reaches it.
		return `<member><LoadBalancerArn>arn:aws:elasticloadbalancing:us-east-1:` + awsAccount + `:loadbalancer/app/` + name +
			`/1</LoadBalancerArn><LoadBalancerName>` + name + `</LoadBalancerName><DNSName>` + strings.TrimPrefix(e.base, "http://") +
			`</DNSName><State><Code>active</Code></State></member>`
	}
	q("DescribeLoadBalancers", func(form url.Values) (int, string) {
		name := form.Get("Names.member.1")
		if name == "" || !e.lbs[name] {
			if name == "" {
				for n := range e.lbs {
					name = n
				}
				return 200, resp("DescribeLoadBalancers", elbNS, `<LoadBalancers>`+lbMember(name)+`</LoadBalancers>`)
			}
			return fail("LoadBalancerNotFound")
		}
		return 200, resp("DescribeLoadBalancers", elbNS, `<LoadBalancers>`+lbMember(name)+`</LoadBalancers>`)
	})
	q("CreateLoadBalancer", func(form url.Values) (int, string) {
		e.lbs[form.Get("Name")] = true
		return 200, resp("CreateLoadBalancer", elbNS, `<LoadBalancers>`+lbMember(form.Get("Name"))+`</LoadBalancers>`)
	})
	q("DescribeTargetGroups", func(form url.Values) (int, string) {
		arn, ok := e.tgs[form.Get("Names.member.1")]
		if !ok {
			return fail("TargetGroupNotFound")
		}
		return 200, resp("DescribeTargetGroups", elbNS, `<TargetGroups><member><TargetGroupArn>`+arn+`</TargetGroupArn></member></TargetGroups>`)
	})
	q("CreateTargetGroup", func(form url.Values) (int, string) {
		arn := "arn:aws:elasticloadbalancing:us-east-1:" + awsAccount + ":targetgroup/" + form.Get("Name") + "/1"
		e.tgs[form.Get("Name")] = arn
		return 200, resp("CreateTargetGroup", elbNS, `<TargetGroups><member><TargetGroupArn>`+arn+`</TargetGroupArn></member></TargetGroups>`)
	})
	q("DescribeListeners", func(url.Values) (int, string) {
		var b strings.Builder
		for _, l := range e.listeners {
			fmt.Fprintf(&b, `<member><ListenerArn>%s</ListenerArn><Port>%s</Port><Protocol>HTTP</Protocol><DefaultActions><member><Type>forward</Type><TargetGroupArn>%s</TargetGroupArn></member></DefaultActions></member>`,
				l["arn"], l["port"], l["tg"])
		}
		return 200, resp("DescribeListeners", elbNS, `<Listeners>`+b.String()+`</Listeners>`)
	})
	q("CreateListener", func(form url.Values) (int, string) {
		arn := fmt.Sprintf("arn:aws:elasticloadbalancing:us-east-1:%s:listener/app/lb/1/%d", awsAccount, len(e.listeners)+1)
		e.listeners = append(e.listeners, map[string]any{"arn": arn, "port": form.Get("Port"), "tg": form.Get("DefaultActions.member.1.TargetGroupArn")})
		return 200, resp("CreateListener", elbNS, `<Listeners><member><ListenerArn>`+arn+`</ListenerArn><Port>`+form.Get("Port")+`</Port></member></Listeners>`)
	})
	q("DeleteListener", func(form url.Values) (int, string) {
		e.listeners = slices.DeleteFunc(e.listeners, func(l map[string]any) bool { return l["arn"] == form.Get("ListenerArn") })
		return 200, resp("DeleteListener", elbNS, ``)
	})
	q("DeleteTargetGroup", func(form url.Values) (int, string) {
		for name, arn := range e.tgs {
			if arn == form.Get("TargetGroupArn") {
				delete(e.tgs, name)
			}
		}
		return 200, resp("DeleteTargetGroup", elbNS, ``)
	})

	// RDS, in the common query dialect too. An instance is creating the
	// first time it's read, then available.
	const rdsNS = "http://rds.amazonaws.com/doc/2014-10-31/"
	dbInstance := func(id string) string {
		in := e.dbs[id]
		status := "creating"
		if e.dbPolls[id] > 1 {
			status = "available"
		}
		port := "5432"
		if in.Get("Engine") == "mysql" {
			port = "3306"
		}
		return `<DBInstance><DBInstanceIdentifier>` + id + `</DBInstanceIdentifier><Engine>` + in.Get("Engine") +
			`</Engine><EngineVersion>` + in.Get("EngineVersion") + `.2</EngineVersion><DBInstanceStatus>` + status +
			`</DBInstanceStatus><Endpoint><Address>` + id + `.abc.us-east-1.rds.amazonaws.com</Address><Port>` + port +
			`</Port></Endpoint></DBInstance>`
	}
	q("DescribeDBInstances", func(form url.Values) (int, string) {
		id := form.Get("DBInstanceIdentifier")
		if _, ok := e.dbs[id]; !ok {
			return fail("DBInstanceNotFound")
		}
		e.dbPolls[id]++
		return 200, resp("DescribeDBInstances", rdsNS, `<DBInstances>`+dbInstance(id)+`</DBInstances>`)
	})
	q("CreateDBInstance", func(form url.Values) (int, string) {
		id := form.Get("DBInstanceIdentifier")
		e.dbs[id] = form
		return 200, resp("CreateDBInstance", rdsNS, dbInstance(id))
	})
	q("CreateDBSubnetGroup", func(form url.Values) (int, string) {
		return 200, resp("CreateDBSubnetGroup", rdsNS, `<DBSubnetGroup><DBSubnetGroupName>`+form.Get("DBSubnetGroupName")+`</DBSubnetGroupName></DBSubnetGroup>`)
	})

	// ElastiCache, in the query dialect.
	const ecNS = "http://elasticache.amazonaws.com/doc/2015-02-02/"
	group := func(id string) string {
		status := "creating"
		if e.cachePolls[id] > 1 {
			status = "available"
		}
		return `<ReplicationGroup><ReplicationGroupId>` + id + `</ReplicationGroupId><Status>` + status +
			`</Status><NodeGroups><NodeGroup><PrimaryEndpoint><Address>` + id + `.cache.amazonaws.com</Address><Port>6379</Port></PrimaryEndpoint></NodeGroup></NodeGroups></ReplicationGroup>`
	}
	q("CreateCacheSubnetGroup", func(form url.Values) (int, string) {
		return 200, resp("CreateCacheSubnetGroup", ecNS, `<CacheSubnetGroup><CacheSubnetGroupName>`+form.Get("CacheSubnetGroupName")+`</CacheSubnetGroupName></CacheSubnetGroup>`)
	})
	q("CreateReplicationGroup", func(form url.Values) (int, string) {
		id := form.Get("ReplicationGroupId")
		e.caches[id] = form
		return 200, resp("CreateReplicationGroup", ecNS, group(id))
	})
	q("DescribeReplicationGroups", func(form url.Values) (int, string) {
		id := form.Get("ReplicationGroupId")
		if _, ok := e.caches[id]; !ok {
			return fail("ReplicationGroupNotFoundFault")
		}
		e.cachePolls[id]++
		return 200, resp("DescribeReplicationGroups", ecNS, `<ReplicationGroups>`+group(id)+`</ReplicationGroups>`)
	})
	q("DeleteReplicationGroup", func(form url.Values) (int, string) {
		id := form.Get("ReplicationGroupId")
		delete(e.caches, id)
		e.deleted = append(e.deleted, "cache:"+id)
		return 200, resp("DeleteReplicationGroup", ecNS, `<ReplicationGroup><ReplicationGroupId>`+id+`</ReplicationGroupId><Status>deleting</Status></ReplicationGroup>`)
	})

	// S3, path style.
	f.rest = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		e.mu.Lock()
		defer e.mu.Unlock()
		parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/"), "/", 2)
		q := r.URL.Query()
		switch {
		case r.Method == http.MethodPut && len(parts) == 1 && q.Has("publicAccessBlock"):
			e.private[parts[0]] = true
		case r.Method == http.MethodPut && len(parts) == 1:
			e.buckets[parts[0]] = true
		case r.Method == http.MethodHead && len(parts) == 1:
			if !e.buckets[parts[0]] {
				w.WriteHeader(http.StatusNotFound)
			}
		case r.Method == http.MethodGet && len(parts) == 1 && q.Get("list-type") == "2":
			w.Header().Set("Content-Type", "application/xml")
			_, _ = w.Write([]byte(`<ListBucketResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><Name>` + parts[0] + `</Name><KeyCount>0</KeyCount><IsTruncated>false</IsTruncated></ListBucketResult>`))
		case r.Method == http.MethodDelete && len(parts) == 1:
			delete(e.buckets, parts[0])
			e.deleted = append(e.deleted, "bucket:"+parts[0])
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPut && len(parts) == 2:
			body, _ := io.ReadAll(r.Body)
			e.uploads[parts[1]] = len(body)
			w.Header().Set("ETag", `"1"`)
		default:
			w.WriteHeader(http.StatusOK)
		}
	})
	return e
}

const apiAndWorkerOnECS = `version: 1
services:
  api:
    source: go:services/api
    provider: ecs-fargate
  worker:
    source: go:services/worker/cmd/worker
    provider: ecs-fargate
    needs: [api]
ignore:
  resources: [postgres, redis]
`

func TestDeploy_servicesRunOnECSFargate(t *testing.T) {
	fake := newFakeAWS(t)
	cloud := installFakeECS(t, fake)
	cfg := config.Defaults()
	cfg.Database.URL = freshDatabase(t)
	cfg.Orchestration.PollInterval = 100 * time.Millisecond
	cfg.GitHub.APIURL = fakeGitHubServing(t).URL
	cfg.Analysis.WorkDir = t.TempDir()
	cfg.Clouds.AWSEndpoint = fake.srv.URL
	cfg.Storage.URL = "file://" + t.TempDir()
	r := start(t, cfg)
	defer r.stop()

	c := &client{t: t, base: r.srv.URL, key: cfg.Identity.PublishableKey}
	var auth struct {
		SessionToken string `json:"session_token"`
	}
	c.must(http.StatusCreated, http.MethodPost, "/authsome/v1/signup", map[string]string{"email": "ada@example.com", "password": "Correct-horse-battery-9"}, &auth)
	c.token = auth.SessionToken
	var org struct {
		ID string `json:"id"`
	}
	c.must(http.StatusCreated, http.MethodPost, "/authsome/v1/orgs", map[string]string{"name": "Acme", "slug": "acme"}, &org)
	c.must(http.StatusOK, http.MethodPost, "/authsome/v1/me/switch-org", map[string]string{"org_id": org.ID}, nil)
	var proj struct {
		ID string `json:"id"`
	}
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/projects", map[string]string{"name": "Mono", "repository_url": "https://github.com/acme/mono"}, &proj)
	base := "/api/v1/projects/" + proj.ID
	analyze(t, cfg, c, proj.ID)
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/connections",
		map[string]any{"cloud": "aws", "name": "AWS", "credential": awsKey(awsKeyID), "settings": map[string]string{"region": "us-east-1"}}, nil)
	const secretValue = "ecs-secret-token"
	c.must(http.StatusCreated, http.MethodPost, base+"/variables",
		map[string]any{"key": "API_TOKEN", "value": secretValue, "secret": true, "environment": "production"}, nil)
	c.must(http.StatusCreated, http.MethodPut, base+"/manifest", map[string]string{"yaml": apiAndWorkerOnECS}, nil)

	var plan planBody
	c.must(http.StatusCreated, http.MethodPost, base+"/plans", map[string]string{"environment": "production"}, &plan)
	waitPlanned(t, c, &plan)
	if plan.Status != "ready" || !plan.Executable || len(plan.Actions) != 2 {
		t.Fatalf("plan %+v", plan)
	}
	// No certificate on the connection: api will serve plain HTTP, and
	// the plan says so.
	if !slices.ContainsFunc(plan.Warnings, func(w struct {
		Code string `json:"code"`
		Name string `json:"name"`
	}) bool {
		return w.Code == "plain-http" && w.Name == "api"
	}) {
		t.Fatalf("no plain-http warning: %+v", plan.Warnings)
	}

	d := approveAndWait(t, c, plan)
	if d.Status != "succeeded" {
		t.Fatalf("deployment %+v", d)
	}
	var api, worker *struct {
		ActionID string `json:"action_id"`
		Status   string `json:"status"`
		Message  string `json:"message"`
		URL      string `json:"url"`
		Image    string `json:"image"`
	}
	for i := range d.Steps {
		switch d.Steps[i].ActionID {
		case "service:api":
			api = &d.Steps[i]
		case "service:worker":
			worker = &d.Steps[i]
		}
	}
	if api.URL != fake.srv.URL || !strings.HasPrefix(api.Image, awsAccount+".dkr.ecr.us-east-1.amazonaws.com/loomplane/api-") || worker.URL != "" {
		t.Fatalf("api %+v, worker %+v", api, worker)
	}

	cloud.mu.Lock()
	defer cloud.mu.Unlock()
	if !cloud.roles["loomplane-codebuild"] || !cloud.roles["loomplane-ecs-task-execution"] || len(cloud.uploads) != 1 {
		t.Fatalf("roles %v, uploads %v", cloud.roles, cloud.uploads)
	}
	// api builds from its Dockerfile; the worker, which has none, with
	// buildpacks.
	specs := make([]string, 0, len(cloud.builds))
	for _, b := range cloud.builds {
		specs = append(specs, b["buildspecOverride"].(string))
	}
	if !slices.ContainsFunc(specs, func(s string) bool {
		return strings.Contains(s, "docker build -t ") && strings.Contains(s, "services/api")
	}) ||
		!slices.ContainsFunc(specs, func(s string) bool {
			return strings.Contains(s, "pack build") && strings.Contains(s, "GOOGLE_BUILDABLE=./services/worker/cmd/worker")
		}) {
		t.Fatalf("buildspecs %q", specs)
	}
	var apiSvc, workerSvc map[string]any
	for _, svc := range cloud.services {
		req := svc["request"].(map[string]any)
		if _, lb := req["loadBalancers"]; lb {
			apiSvc = req
		} else {
			workerSvc = req
		}
	}
	if apiSvc == nil || workerSvc == nil || apiSvc["launchType"] != "FARGATE" {
		t.Fatalf("services %+v", cloud.services)
	}
	if lb := apiSvc["loadBalancers"].([]any)[0].(map[string]any); lb["containerPort"] != float64(8080) {
		t.Fatalf("api's load balancer %+v", lb)
	}
	// Both join the environment's namespace; api is reached there as
	// api:8080, and worker, with no port, only calls out.
	if len(cloud.namespaces) != 1 {
		t.Fatalf("namespaces %v", cloud.namespaces)
	}
	apiSC, workerSC := apiSvc["serviceConnectConfiguration"].(map[string]any), workerSvc["serviceConnectConfiguration"].(map[string]any)
	alias := apiSC["services"].([]any)[0].(map[string]any)["clientAliases"].([]any)[0].(map[string]any)
	if apiSC["enabled"] != true || alias["dnsName"] != "api" || alias["port"] != float64(8080) ||
		workerSC["enabled"] != true || workerSC["namespace"] != apiSC["namespace"] || workerSC["services"] != nil {
		t.Fatalf("service connect: api %+v, worker %+v", apiSC, workerSC)
	}
	// The secret went to Secrets Manager and reaches the task by ARN.
	var apiTask map[string]any
	for _, td := range cloud.tasks {
		ctr := td["containerDefinitions"].([]any)[0].(map[string]any)
		if strings.Contains(ctr["image"].(string), "/loomplane/api-") {
			apiTask = ctr
		}
	}
	// worker reaches api privately, not through the load balancer.
	var workerTask map[string]any
	for _, td := range cloud.tasks {
		ctr := td["containerDefinitions"].([]any)[0].(map[string]any)
		if strings.Contains(ctr["image"].(string), "/loomplane/worker-") {
			workerTask = ctr
		}
	}
	if !slices.ContainsFunc(workerTask["environment"].([]any), func(v any) bool {
		kv := v.(map[string]any)
		return kv["name"] == "API_URL" && kv["value"] == "http://api:8080"
	}) {
		t.Fatalf("worker's environment %+v", workerTask["environment"])
	}
	sec := apiTask["secrets"].([]any)[0].(map[string]any)
	if sec["name"] != "API_TOKEN" || !strings.HasPrefix(sec["valueFrom"].(string), "arn:aws:secretsmanager:") {
		t.Fatalf("api's secrets %+v", apiTask["secrets"])
	}
	if raw, _ := json.Marshal(apiTask); strings.Contains(string(raw), secretValue) {
		t.Fatal("the secret is in the task definition")
	}
	found := false
	for _, v := range cloud.secrets {
		found = found || v == secretValue
	}
	if !found {
		t.Fatalf("secrets manager holds %v", cloud.secrets)
	}
}

const apiOnECSWithRDS = `version: 1
services:
  api:
    source: go:services/api
    provider: ecs-fargate
    needs: [db]
resources:
  db:
    kind: postgres
    provider: rds
ignore:
  resources: [redis]
`

func TestDeploy_anECSServiceGetsItsRDSDatabase(t *testing.T) {
	fake := newFakeAWS(t)
	cloud := installFakeECS(t, fake)
	cfg := config.Defaults()
	cfg.Database.URL = freshDatabase(t)
	cfg.Orchestration.PollInterval = 100 * time.Millisecond
	cfg.GitHub.APIURL = fakeGitHubServing(t).URL
	cfg.Analysis.WorkDir = t.TempDir()
	cfg.Clouds.AWSEndpoint = fake.srv.URL
	cfg.Storage.URL = "file://" + t.TempDir()
	r := start(t, cfg)
	defer r.stop()

	c := &client{t: t, base: r.srv.URL, key: cfg.Identity.PublishableKey}
	var auth struct {
		SessionToken string `json:"session_token"`
	}
	c.must(http.StatusCreated, http.MethodPost, "/authsome/v1/signup", map[string]string{"email": "ada@example.com", "password": "Correct-horse-battery-9"}, &auth)
	c.token = auth.SessionToken
	var org struct {
		ID string `json:"id"`
	}
	c.must(http.StatusCreated, http.MethodPost, "/authsome/v1/orgs", map[string]string{"name": "Acme", "slug": "acme"}, &org)
	c.must(http.StatusOK, http.MethodPost, "/authsome/v1/me/switch-org", map[string]string{"org_id": org.ID}, nil)
	var proj struct {
		ID string `json:"id"`
	}
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/projects", map[string]string{"name": "Mono", "repository_url": "https://github.com/acme/mono"}, &proj)
	base := "/api/v1/projects/" + proj.ID
	analyze(t, cfg, c, proj.ID)
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/connections",
		map[string]any{"cloud": "aws", "name": "AWS", "credential": awsKey(awsKeyID), "settings": map[string]string{"region": "us-east-1"}}, nil)
	c.must(http.StatusCreated, http.MethodPut, base+"/manifest", map[string]string{"yaml": apiOnECSWithRDS}, nil)

	var plan planBody
	c.must(http.StatusCreated, http.MethodPost, base+"/plans", map[string]string{"environment": "production"}, &plan)
	waitPlanned(t, c, &plan)
	if plan.Status != "ready" || !plan.Executable || len(plan.Actions) != 2 || plan.Actions[0].ID != "resource:db" {
		t.Fatalf("plan %+v", plan)
	}
	d := approveAndWait(t, c, plan)
	if d.Status != "succeeded" || d.Steps[0].ActionID != "resource:db" || d.Steps[0].Status != "succeeded" {
		t.Fatalf("deployment %+v", d)
	}

	cloud.mu.Lock()
	if len(cloud.dbs) != 1 {
		t.Fatalf("%d rds instances", len(cloud.dbs))
	}
	var id string
	var in url.Values
	for k, v := range cloud.dbs {
		id, in = k, v
	}
	password := in.Get("MasterUserPassword")
	if password == "" || in.Get("Engine") != "postgres" || in.Get("EngineVersion") != "17" || in.Get("DBName") != "db" ||
		in.Get("PubliclyAccessible") != "false" || in.Get("MasterUsername") != "app" {
		t.Fatalf("instance %s: %v", id, in)
	}
	// The database's group lets in the environment's tasks, on its port,
	// and nobody else.
	dbGroup := in.Get("VpcSecurityGroupIds.VpcSecurityGroupId.1")
	tasks := ""
	for name, g := range cloud.groups {
		if strings.HasSuffix(name, "-tasks") {
			tasks = g
		}
	}
	if tasks == "" || !slices.Equal(cloud.ingress[dbGroup], []string{tasks + ":5432"}) {
		t.Fatalf("db group %s takes %v; tasks are %s", dbGroup, cloud.ingress[dbGroup], tasks)
	}
	// api reads DATABASE_URL from Secrets Manager, by ARN.
	ctr := cloud.tasks[len(cloud.tasks)-1]["containerDefinitions"].([]any)[0].(map[string]any)
	var arn string
	for _, sec := range ctr["secrets"].([]any) {
		if m := sec.(map[string]any); m["name"] == "DATABASE_URL" {
			arn = m["valueFrom"].(string)
		}
	}
	got := ""
	for name, v := range cloud.secrets {
		if arn != "" && strings.Contains(arn, ":secret:"+name+"-") {
			got = v
		}
	}
	cloud.mu.Unlock()
	want := "postgresql://app:" + password + "@" + id + ".abc.us-east-1.rds.amazonaws.com:5432/db?sslmode=require"
	if got != want {
		t.Fatalf("DATABASE_URL (%s) is %q, want %q", arn, got, want)
	}

	// Planning again finds both as deployed: RDS's minor version isn't
	// drift.
	var again planBody
	c.must(http.StatusCreated, http.MethodPost, base+"/plans", map[string]string{"environment": "production"}, &again)
	waitPlanned(t, c, &again)
	for _, a := range again.Actions {
		if a.Op != "keep" {
			t.Fatalf("second plan %+v", again)
		}
	}
}

const gatewayOnECS = `version: 1
defaults: {provider: ecs-fargate}
services:
  api:
    source: go:services/api
    public: false
gateways:
  edge:
    routes:
      - {path: /api, to: api, strip_prefix: true}
ignore:
  resources: [postgres, redis]
`

func TestDeploy_aGatewayRoutesToAPrivateService(t *testing.T) {
	fake := newFakeAWS(t)
	cloud := installFakeECS(t, fake)
	cfg := config.Defaults()
	cfg.Database.URL = freshDatabase(t)
	cfg.Orchestration.PollInterval = 100 * time.Millisecond
	cfg.GitHub.APIURL = fakeGitHubServing(t).URL
	cfg.Analysis.WorkDir = t.TempDir()
	cfg.Clouds.AWSEndpoint = fake.srv.URL
	cfg.Storage.URL = "file://" + t.TempDir()
	r := start(t, cfg)
	defer r.stop()

	c := &client{t: t, base: r.srv.URL, key: cfg.Identity.PublishableKey}
	var auth struct {
		SessionToken string `json:"session_token"`
	}
	c.must(http.StatusCreated, http.MethodPost, "/authsome/v1/signup", map[string]string{"email": "ada@example.com", "password": "Correct-horse-battery-9"}, &auth)
	c.token = auth.SessionToken
	var org struct {
		ID string `json:"id"`
	}
	c.must(http.StatusCreated, http.MethodPost, "/authsome/v1/orgs", map[string]string{"name": "Acme", "slug": "acme"}, &org)
	c.must(http.StatusOK, http.MethodPost, "/authsome/v1/me/switch-org", map[string]string{"org_id": org.ID}, nil)
	var proj struct {
		ID string `json:"id"`
	}
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/projects", map[string]string{"name": "Mono", "repository_url": "https://github.com/acme/mono"}, &proj)
	base := "/api/v1/projects/" + proj.ID
	analyze(t, cfg, c, proj.ID)
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/connections",
		map[string]any{"cloud": "aws", "name": "AWS", "credential": awsKey(awsKeyID), "settings": map[string]string{"region": "us-east-1"}}, nil)
	c.must(http.StatusCreated, http.MethodPost, base+"/variables",
		map[string]any{"key": "API_TOKEN", "value": "not-for-the-gateway", "secret": true, "environment": "production"}, nil)
	c.must(http.StatusCreated, http.MethodPut, base+"/manifest", map[string]string{"yaml": gatewayOnECS}, nil)

	var plan planBody
	c.must(http.StatusCreated, http.MethodPost, base+"/plans", map[string]string{"environment": "production"}, &plan)
	waitPlanned(t, c, &plan)
	if plan.Status != "ready" || !plan.Executable || len(plan.Actions) != 2 || plan.Actions[1].ID != "service:edge" {
		t.Fatalf("plan %+v", plan)
	}
	d := approveAndWait(t, c, plan)
	if d.Status != "succeeded" {
		t.Fatalf("deployment %+v", d)
	}

	cloud.mu.Lock()
	defer cloud.mu.Unlock()
	// The gateway's generated source went up beside the commit's, not
	// over it, and builds from its own Dockerfile.
	var gw string
	for key := range cloud.uploads {
		if strings.HasPrefix(key, "sources/gw") {
			gw = key
		}
	}
	if len(cloud.uploads) != 2 || gw == "" {
		t.Fatalf("uploads %v", cloud.uploads)
	}
	if !slices.ContainsFunc(cloud.builds, func(b map[string]any) bool {
		return strings.Contains(b["sourceLocationOverride"].(string), gw) && strings.Contains(b["buildspecOverride"].(string), "docker build -t ") &&
			strings.Contains(b["buildspecOverride"].(string), "/loomplane/edge-")
	}) {
		t.Fatalf("no gateway build from %s: %+v", gw, cloud.builds)
	}
	// It reaches api inside the environment, and gets nothing else.
	var edge map[string]any
	for _, td := range cloud.tasks {
		ctr := td["containerDefinitions"].([]any)[0].(map[string]any)
		if strings.Contains(ctr["image"].(string), "/loomplane/edge-") {
			edge = ctr
		}
	}
	env := edge["environment"].([]any)
	if len(env) != 1 || env[0].(map[string]any)["name"] != "API_URL" || env[0].(map[string]any)["value"] != "http://api:8080" || edge["secrets"] != nil {
		t.Fatalf("edge's environment %+v, secrets %+v", env, edge["secrets"])
	}
	// The gateway is public, behind the load balancer; api isn't.
	public := 0
	for _, svc := range cloud.services {
		if _, lb := svc["request"].(map[string]any)["loadBalancers"]; lb {
			public++
		}
	}
	if public != 1 {
		t.Fatalf("%d services behind the load balancer", public)
	}
}

const apiOnECSWithStorageAndCache = `version: 1
services:
  api:
    source: go:services/api
    provider: ecs-fargate
    needs: [files, cache]
resources:
  files:
    kind: object-storage
    provider: s3
  cache:
    kind: redis
    provider: elasticache
ignore:
  resources: [postgres]
`

func TestDeploy_anECSServiceGetsAPrivateBucketAndCache(t *testing.T) {
	fake := newFakeAWS(t)
	cloud := installFakeECS(t, fake)
	cfg := config.Defaults()
	cfg.Database.URL = freshDatabase(t)
	cfg.Orchestration.PollInterval = 100 * time.Millisecond
	cfg.GitHub.APIURL = fakeGitHubServing(t).URL
	cfg.Analysis.WorkDir = t.TempDir()
	cfg.Clouds.AWSEndpoint = fake.srv.URL
	cfg.Storage.URL = "file://" + t.TempDir()
	r := start(t, cfg)
	defer r.stop()

	c := &client{t: t, base: r.srv.URL, key: cfg.Identity.PublishableKey}
	var auth struct {
		SessionToken string `json:"session_token"`
	}
	c.must(http.StatusCreated, http.MethodPost, "/authsome/v1/signup", map[string]string{"email": "ada@example.com", "password": "Correct-horse-battery-9"}, &auth)
	c.token = auth.SessionToken
	var org struct {
		ID string `json:"id"`
	}
	c.must(http.StatusCreated, http.MethodPost, "/authsome/v1/orgs", map[string]string{"name": "Acme", "slug": "acme"}, &org)
	c.must(http.StatusOK, http.MethodPost, "/authsome/v1/me/switch-org", map[string]string{"org_id": org.ID}, nil)
	var proj struct {
		ID string `json:"id"`
	}
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/projects", map[string]string{"name": "Mono", "repository_url": "https://github.com/acme/mono"}, &proj)
	base := "/api/v1/projects/" + proj.ID
	analyze(t, cfg, c, proj.ID)
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/connections",
		map[string]any{"cloud": "aws", "name": "AWS", "credential": awsKey(awsKeyID), "settings": map[string]string{"region": "us-east-1"}}, nil)
	c.must(http.StatusCreated, http.MethodPut, base+"/manifest", map[string]string{"yaml": apiOnECSWithStorageAndCache}, nil)

	var plan planBody
	c.must(http.StatusCreated, http.MethodPost, base+"/plans", map[string]string{"environment": "production"}, &plan)
	waitPlanned(t, c, &plan)
	if plan.Status != "ready" || !plan.Executable || len(plan.Actions) != 3 {
		t.Fatalf("plan %+v", plan)
	}
	if d := approveAndWait(t, c, plan); d.Status != "succeeded" {
		t.Fatalf("deployment %+v", d)
	}

	cloud.mu.Lock()
	var bucket string
	for name := range cloud.private {
		bucket = name
	}
	if bucket == "" || !strings.HasPrefix(bucket, "lp-files-") || !cloud.buckets[bucket] {
		t.Fatalf("buckets %v, private %v", cloud.buckets, cloud.private)
	}
	// The environment's tasks run as a role that reaches its buckets, and
	// api runs as it.
	var role, policy string
	for name, p := range cloud.policies {
		if strings.HasSuffix(name, "-tasks") {
			role, policy = name, p
		}
	}
	key := bucket[strings.LastIndex(bucket, "-")+1:]
	if !strings.Contains(policy, "arn:aws:s3:::lp-*-"+key) {
		t.Fatalf("role %s's policy %s", role, policy)
	}
	ctr := cloud.tasks[len(cloud.tasks)-1]
	if ctr["taskRoleArn"] != "arn:aws:iam::"+awsAccount+":role/"+role {
		t.Fatalf("api runs as %v", ctr["taskRoleArn"])
	}
	// One Redis, encrypted, only the tasks can reach.
	if len(cloud.caches) != 1 {
		t.Fatalf("caches %v", cloud.caches)
	}
	var cacheID string
	var cache url.Values
	for id, v := range cloud.caches {
		cacheID, cache = id, v
	}
	token := cache.Get("AuthToken")
	if token == "" || cache.Get("TransitEncryptionEnabled") != "true" || cache.Get("AtRestEncryptionEnabled") != "true" {
		t.Fatalf("cache %v", cache)
	}
	cacheGroup := cache.Get("SecurityGroupIds.SecurityGroupId.1")
	if ingress := cloud.ingress[cacheGroup]; len(ingress) != 1 || !strings.HasSuffix(ingress[0], ":6379") {
		t.Fatalf("the cache's group takes %v", ingress)
	}
	// api finds both through Secrets Manager.
	got := map[string]string{}
	container := ctr["containerDefinitions"].([]any)[0].(map[string]any)
	for _, sec := range container["secrets"].([]any) {
		m := sec.(map[string]any)
		for name, v := range cloud.secrets {
			if strings.Contains(m["valueFrom"].(string), ":secret:"+name+"-") {
				got[m["name"].(string)] = v
			}
		}
	}
	cloud.mu.Unlock()
	if got["STORAGE_URL"] != "s3://"+bucket+"?region=us-east-1" || got["REDIS_URL"] != "rediss://:"+token+"@"+cacheID+".cache.amazonaws.com:6379" {
		t.Fatalf("api's secrets %v", got)
	}

	// Planning again keeps all three.
	var again planBody
	c.must(http.StatusCreated, http.MethodPost, base+"/plans", map[string]string{"environment": "production"}, &again)
	waitPlanned(t, c, &again)
	for _, a := range again.Actions {
		if a.Op != "keep" {
			t.Fatalf("second plan %+v", again)
		}
	}

	// Dropping them deletes the bucket and the cache.
	c.must(http.StatusCreated, http.MethodPut, base+"/manifest", map[string]string{"yaml": "version: 1\nservices:\n  api:\n    source: go:services/api\n    provider: ecs-fargate\nignore:\n  resources: [postgres, redis]\n"}, nil)
	var removal planBody
	c.must(http.StatusCreated, http.MethodPost, base+"/plans", map[string]string{"environment": "production"}, &removal)
	waitPlanned(t, c, &removal)
	if d := approveAndWait(t, c, removal); d.Status != "succeeded" {
		t.Fatalf("removal %+v", d)
	}
	cloud.mu.Lock()
	defer cloud.mu.Unlock()
	if !slices.Contains(cloud.deleted, "bucket:"+bucket) || !slices.Contains(cloud.deleted, "cache:"+cacheID) {
		t.Fatalf("deleted %v", cloud.deleted)
	}
}
