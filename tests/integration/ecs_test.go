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
}

const ecsAPI = "AmazonEC2ContainerServiceV20141113."

func installFakeECS(t *testing.T, f *fakeAWS) *fakeECS {
	t.Helper()
	e := &fakeECS{
		base: f.srv.URL, repos: map[string]string{}, polls: map[string]int{}, roles: map[string]bool{},
		groups: map[string]string{}, lbs: map[string]bool{}, tgs: map[string]string{},
		services: map[string]map[string]any{}, secrets: map[string]string{}, buckets: map[string]bool{}, uploads: map[string]int{},
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
		return 200, map[string]any{"builds": []any{map[string]any{"id": id, "buildStatus": status, "currentPhase": "BUILD"}}}
	})
	// Logs and Secrets Manager.
	j("Logs_20140328.CreateLogGroup", func(map[string]any) (int, any) { return 200, map[string]any{} })
	j("secretsmanager.CreateSecret", func(in map[string]any) (int, any) {
		name := in["Name"].(string)
		e.secrets[name] = in["SecretString"].(string)
		return 200, map[string]any{"ARN": "arn:aws:secretsmanager:us-east-1:" + awsAccount + ":secret:" + name + "-AbCdEf", "Name": name}
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
	q("AuthorizeSecurityGroupIngress", func(url.Values) (int, string) {
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
	q("PutRolePolicy", func(url.Values) (int, string) { return 200, resp("PutRolePolicy", iamNS, "") })
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

	// S3, path style.
	f.rest = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		e.mu.Lock()
		defer e.mu.Unlock()
		parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/"), "/", 2)
		switch {
		case r.Method == http.MethodPut && len(parts) == 1:
			e.buckets[parts[0]] = true
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
	// The secret went to Secrets Manager and reaches the task by ARN.
	var apiTask map[string]any
	for _, td := range cloud.tasks {
		ctr := td["containerDefinitions"].([]any)[0].(map[string]any)
		if strings.Contains(ctr["image"].(string), "/loomplane/api-") {
			apiTask = ctr
		}
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
