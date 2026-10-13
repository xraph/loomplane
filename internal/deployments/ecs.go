package deployments

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/sha1" //nolint:gosec // G505: names a target group, nothing secret
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/xraph/ctrlplane/deploy"
	"github.com/xraph/ctrlplane/id"
	"github.com/xraph/ctrlplane/instance"
	cpprovider "github.com/xraph/ctrlplane/provider"

	"github.com/xraph/loomplane/internal/clouds/aws"
	"github.com/xraph/loomplane/internal/clouds/ecs"
	"github.com/xraph/loomplane/internal/clouds/progress"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/infrastructure"
	"github.com/xraph/loomplane/internal/planning"
)

const (
	ecsCluster      = "loomplane"
	ecsBuildProject = "loomplane"
	codeBuildRole   = "loomplane-codebuild"
	ecsTaskRole     = "loomplane-ecs-task-execution"
	packVersion     = "v0.36.4"
	ecsExecPolicy   = "arn:aws:iam::aws:policy/service-role/AmazonECSTaskExecutionRolePolicy"
)

// ECS builds a service on CodeBuild into ECR and runs it on ECS with
// Fargate, as a Ctrl Plane instance, in the account's default VPC. A
// public service gets a listener of its own on the environment's load
// balancer. Nothing from the repository runs on a Loomplane machine.
type ECS struct {
	AWS         *aws.Client
	Ctrl        CtrlPlane
	Credentials Credentials
}

var _ Executor = (*ECS)(nil)

type ecsState struct {
	BuildID string `json:"build_id,omitempty"`
	Image   string `json:"image,omitempty"`
}

func (e *ECS) session(job *Job, cred []byte) (*aws.Session, error) {
	key, err := aws.ParseKey(cred)
	if err != nil {
		return nil, err
	}
	return e.AWS.Session(key, job.Connection.Details["region"], job.Connection.Account), nil
}

// Apply implements Executor.
func (e *ECS) Apply(ctx context.Context, job *Job) (*Outcome, error) {
	if job.Action.Service == nil {
		return nil, fmt.Errorf("%w: ecs runs services, not %s", ErrNoExecutor, job.Action.Subject)
	}
	s, err := e.session(job, job.Credential)
	if err != nil {
		return nil, err
	}
	var state ecsState
	if len(job.Resume) > 0 {
		_ = json.Unmarshal(job.Resume, &state) //nolint:errcheck // an unreadable checkpoint starts over
	}
	save := func() {
		data, _ := json.Marshal(state) //nolint:errcheck // a plain struct always encodes
		_ = job.Checkpoint(data)       //nolint:errcheck // the next heartbeat retries it
	}
	status := StepBuilding
	ctx = progress.With(ctx, func(m string) {
		job.Progress(status, m)
		save()
	})
	if state.Image == "" {
		if state.Image, err = e.build(ctx, s, job, &state, save); err != nil {
			return nil, err
		}
		save()
	}
	status = StepDeploying
	job.Progress(StepDeploying, "Deploying "+state.Image)
	ext, url, err := e.run(ctx, s, job, state.Image)
	if err != nil {
		return nil, err
	}
	return &Outcome{ExternalID: ext, URL: url, Image: state.Image, Revision: state.Image}, nil
}

func sourceBucket(s *aws.Session) string { return "loomplane-" + s.Account() + "-" + s.Region() }

// build runs the service's build on CodeBuild and returns its image.
func (e *ECS) build(ctx context.Context, s *aws.Session, job *Job, state *ecsState, save func()) (string, error) {
	cfg := job.Action.Service
	tag, err := imageTag(job)
	if err != nil {
		return "", err
	}
	repo, err := s.EnsureRepository(ctx, "loomplane/"+imageName(job))
	if err != nil {
		return "", err
	}
	image := repo + ":" + tag
	if state.BuildID == "" {
		bucket := sourceBucket(s)
		job.Progress(StepBuilding, "Uploading the source to CodeBuild")
		if err := s.EnsureBucket(ctx, bucket); err != nil {
			return "", err
		}
		object := "sources/" + job.SourceID + ".zip"
		if err := uploadZip(ctx, s, job, bucket, object); err != nil {
			return "", err
		}
		role, err := s.EnsureRole(ctx, codeBuildRole, "codebuild.amazonaws.com", nil, codeBuildPolicy(s, bucket))
		if err != nil {
			return "", err
		}
		if perr := s.EnsureBuildProject(ctx, ecsBuildProject, role, bucket); perr != nil {
			return "", perr
		}
		if state.BuildID, err = s.StartBuild(ctx, ecsBuildProject, bucket, object, buildspec(s, cfg, image)); err != nil {
			return "", err
		}
		save()
	}
	job.Progress(StepBuilding, "Building on CodeBuild")
	if err := s.WaitBuild(ctx, state.BuildID); err != nil {
		return "", err
	}
	return image, nil
}

// codeBuildPolicy lets CodeBuild read the sources, push to Loomplane's
// repositories, and write its logs. Nothing else.
func codeBuildPolicy(s *aws.Session, bucket string) string {
	return `{"Version":"2012-10-17","Statement":[` +
		`{"Effect":"Allow","Action":["logs:CreateLogGroup","logs:CreateLogStream","logs:PutLogEvents"],"Resource":"*"},` +
		`{"Effect":"Allow","Action":["s3:GetObject","s3:GetObjectVersion"],"Resource":"arn:aws:s3:::` + bucket + `/*"},` +
		`{"Effect":"Allow","Action":["ecr:GetAuthorizationToken"],"Resource":"*"},` +
		`{"Effect":"Allow","Action":["ecr:BatchCheckLayerAvailability","ecr:InitiateLayerUpload","ecr:UploadLayerPart",` +
		`"ecr:CompleteLayerUpload","ecr:PutImage","ecr:BatchGetImage","ecr:GetDownloadUrlForLayer"],` +
		`"Resource":"arn:aws:ecr:` + s.Region() + `:` + s.Account() + `:repository/loomplane/*"}]}`
}

// buildspec builds the image with its Dockerfile, from its build context,
// or with Google's buildpacks through pack, the same way Cloud Build does,
// and pushes it.
func buildspec(s *aws.Session, cfg *planning.ServiceConfig, image string) string {
	registry := s.Account() + ".dkr.ecr." + s.Region() + ".amazonaws.com"
	login := "aws ecr get-login-password --region " + s.Region() + " | docker login --username AWS --password-stdin " + registry
	var install, build []string
	if cfg.Image != "" {
		build = []string{
			`cd "$CODEBUILD_SRC_DIR/` + cfg.ContextDir() + `" && docker build -t ` + image + ` -f ` + cfg.DockerfileIn(cfg.ContextDir()) + ` .`,
			"docker push " + image,
		}
	} else {
		install = []string{"curl -sSL https://github.com/buildpacks/pack/releases/download/" + packVersion + "/pack-" + packVersion +
			"-linux.tgz | tar -C /usr/local/bin -xz pack"}
		args := "pack build " + image + " --builder " + googleBuilder + " --publish"
		switch {
		case cfg.Ecosystem == "go":
			args += " --env GOOGLE_BUILDABLE=./" + cfg.Source + ` --path "$CODEBUILD_SRC_DIR"`
		case cfg.Ecosystem == "jvm" && cfg.Source != ".":
			args += " --env GOOGLE_BUILDABLE=" + cfg.Source + ` --path "$CODEBUILD_SRC_DIR"`
		default:
			args += ` --path "$CODEBUILD_SRC_DIR/` + cfg.Source + `"`
		}
		if cfg.Ecosystem == "python" && cfg.Start != "" {
			args += " --env " + shellQuote("GOOGLE_ENTRYPOINT="+cfg.Start)
		}
		build = []string{args}
	}
	var b strings.Builder
	b.WriteString("version: 0.2\nphases:\n")
	if len(install) > 0 {
		b.WriteString("  install:\n    commands:\n")
		for _, c := range install {
			b.WriteString("      - " + quote(c) + "\n")
		}
	}
	b.WriteString("  pre_build:\n    commands:\n      - " + quote(login) + "\n")
	b.WriteString("  build:\n    commands:\n")
	for _, c := range build {
		b.WriteString("      - " + quote(c) + "\n")
	}
	return b.String()
}

// shellQuote makes s one word for the shell, whatever it holds.
func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// quote makes a command a YAML string, whatever it holds.
func quote(s string) string {
	data, _ := json.Marshal(s) //nolint:errcheck // a string always encodes
	return string(data)
}

// uploadZip repacks the commit's tarball as the zip CodeBuild reads,
// without GitHub's top-level directory, and uploads it.
func uploadZip(ctx context.Context, s *aws.Session, job *Job, bucket, object string) error {
	f, err := os.CreateTemp("", "loomplane-source-*.zip")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if zerr := tarToZip(ctx, job, f); zerr != nil {
		return zerr
	}
	size, err := f.Seek(0, io.SeekCurrent)
	if err != nil {
		return err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	return s.Upload(ctx, bucket, object, f, size)
}

func tarToZip(ctx context.Context, job *Job, w io.Writer) error {
	src, _, err := job.Source(ctx)
	if err != nil {
		return err
	}
	defer src.Close() // read-only
	zr, err := gzip.NewReader(src)
	if err != nil {
		return fmt.Errorf("read the source: %w", err)
	}
	defer zr.Close() // read-only
	tr := tar.NewReader(zr)
	zw := zip.NewWriter(w)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("read the source: %w", err)
		}
		if h.Typeflag != tar.TypeReg {
			continue
		}
		_, rel, ok := strings.Cut(strings.TrimPrefix(h.Name, "./"), "/")
		// Nothing that would land outside the directory CodeBuild unpacks
		// into: an absolute path, or one climbing out with "..".
		if !ok || rel == "" || !filepath.IsLocal(rel) {
			continue
		}
		fh := &zip.FileHeader{Name: rel, Method: zip.Deflate}
		fh.SetMode(os.FileMode(h.Mode)) //nolint:gosec // G115: tar modes fit
		out, err := zw.CreateHeader(fh)
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, tr); err != nil { //nolint:gosec // G110: bounded by the tarball's own size limit
			return err
		}
	}
	return zw.Close()
}

// executionPolicy lets ECS read the service's secrets and nothing else.
func executionPolicy(s *aws.Session) string {
	return `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["secretsmanager:GetSecretValue"],` +
		`"Resource":"arn:aws:secretsmanager:` + s.Region() + `:` + s.Account() + `:secret:loomplane/*"}]}`
}

// shortName fits name in AWS's 32 characters for load balancers and
// target groups, keeping it unique with a hash of the whole.
func shortName(name string) string {
	const prefix = "lp"
	sum := sha1.Sum([]byte(name)) //nolint:gosec // G401: a name, not a secret
	h := hex.EncodeToString(sum[:])[:8]
	room := 32 - len(prefix) - len(h) - 2
	readable := strings.Trim(strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			return r
		}
		return '-'
	}, strings.ToLower(name)), "-")
	if len(readable) > room {
		readable = strings.TrimRight(readable[:room], "-")
	}
	return prefix + "-" + readable + "-" + h
}

// awsEnvironment names what an environment's services share in an AWS
// account: its load balancer and security groups.
func awsEnvironment(job *Job) string {
	return job.Deployment.Environment + "-" + idSuffix(job.Deployment.ProjectID)
}

// tasksGroup is the security group an environment's tasks run in, the
// one its databases let in.
func tasksGroup(ctx context.Context, s *aws.Session, vpc, env string) (string, error) {
	return s.EnsureSecurityGroup(ctx, vpc, "loomplane-"+env+"-tasks", "Loomplane tasks for "+env)
}

// expose puts a public service behind the environment's load balancer:
// its own target group, and a listener that forwards to it.
func expose(ctx context.Context, s *aws.Session, job *Job, net *aws.Network, lbGroup, envName string) (tg, listener, url string, err error) {
	lb, err := s.EnsureLoadBalancer(ctx, shortName(envName), net.Subnets, lbGroup)
	if err != nil {
		return "", "", "", err
	}
	port := int32(job.Action.Service.Port) //nolint:gosec // G115: a port, validated by the manifest
	if tg, err = s.EnsureTargetGroup(ctx, shortName(instanceSlug(job)), net.VPC, port); err != nil {
		return "", "", "", err
	}
	l, err := s.EnsureListener(ctx, lb.ARN, tg, job.Connection.Details["certificate_arn"])
	if err != nil {
		return "", "", "", err
	}
	if aerr := s.AllowFromAnywhere(ctx, lbGroup, l.Port); aerr != nil {
		return "", "", "", aerr
	}
	return tg, l.ARN, listenerURL(lb.DNS, l), nil
}

// LinkedRange is the other cloud's network a link reaches AWS from:
// Google Cloud's default network, in auto mode.
const LinkedRange = "10.128.0.0/9"

// internalGroup is the security group of an environment's internal load
// balancer, which a link lets the other cloud into.
func internalGroup(ctx context.Context, s *aws.Session, vpc, env string) (string, error) {
	return s.EnsureSecurityGroup(ctx, vpc, "loomplane-"+env+"-ilb", "Loomplane internal load balancer for "+env)
}

// exposeInternally puts a private service behind the environment's
// internal load balancer, which has only private addresses: a service on
// the other cloud reaches it over a link. Its name resolves anywhere, to
// those addresses.
func exposeInternally(ctx context.Context, s *aws.Session, job *Job, net *aws.Network, taskGroup, envName string) (tg, listener, url string, err error) {
	group, err := internalGroup(ctx, s, net.VPC, envName)
	if err != nil {
		return "", "", "", err
	}
	if aerr := s.AllowFromGroup(ctx, taskGroup, group); aerr != nil {
		return "", "", "", aerr
	}
	if aerr := s.AllowFromCIDR(ctx, group, LinkedRange); aerr != nil {
		return "", "", "", aerr
	}
	lb, err := s.EnsureInternalLoadBalancer(ctx, shortName(envName+"-in"), net.Subnets, group)
	if err != nil {
		return "", "", "", err
	}
	port := int32(job.Action.Service.Port) //nolint:gosec // G115: a port, validated by the manifest
	if tg, err = s.EnsureTargetGroup(ctx, shortName(instanceSlug(job)), net.VPC, port); err != nil {
		return "", "", "", err
	}
	l, err := s.EnsureListener(ctx, lb.ARN, tg, "")
	if err != nil {
		return "", "", "", err
	}
	return tg, l.ARN, listenerURL(lb.DNS, l), nil
}

// run sets up the environment's network and load balancer, the service's
// secrets, and runs it through Ctrl Plane.
func (e *ECS) run(ctx context.Context, s *aws.Session, job *Job, image string) (externalID, url string, err error) {
	cfg := job.Action.Service
	net, err := s.DefaultNetwork(ctx)
	if err != nil {
		return "", "", err
	}
	if cerr := s.EnsureCluster(ctx, ecsCluster); cerr != nil {
		return "", "", cerr
	}
	execRole, err := s.EnsureRole(ctx, ecsTaskRole, "ecs-tasks.amazonaws.com", []string{ecsExecPolicy}, executionPolicy(s))
	if err != nil {
		return "", "", err
	}
	roleName, rolePolicy := tasksRole(s, job)
	appRole, err := s.EnsureRole(ctx, roleName, "ecs-tasks.amazonaws.com", nil, rolePolicy)
	if err != nil {
		return "", "", err
	}
	logGroup := "/loomplane/" + instanceSlug(job)
	if lerr := s.EnsureLogGroup(ctx, logGroup); lerr != nil {
		return "", "", lerr
	}
	envName := awsEnvironment(job)
	lbGroup, err := s.EnsureSecurityGroup(ctx, net.VPC, "loomplane-"+envName+"-lb", "Loomplane load balancer for "+envName)
	if err != nil {
		return "", "", err
	}
	taskGroup, err := tasksGroup(ctx, s, net.VPC, envName)
	if err != nil {
		return "", "", err
	}
	if aerr := s.AllowFromGroup(ctx, taskGroup, lbGroup); aerr != nil {
		return "", "", aerr
	}
	// The environment's tasks reach each other, privately, by name.
	if aerr := s.AllowFromGroup(ctx, taskGroup, taskGroup); aerr != nil {
		return "", "", aerr
	}
	namespace, err := s.EnsureNamespace(ctx, "loomplane-"+envName)
	if err != nil {
		return "", "", err
	}
	labels := map[string]string{
		ecs.LabelCluster: ecsCluster, ecs.LabelSubnets: strings.Join(net.Subnets, ","), ecs.LabelGroup: taskGroup,
		ecs.LabelRole: execRole, ecs.LabelLogGroup: logGroup, ecs.LabelDesired: "1",
		ecs.LabelNamespace: namespace, ecs.LabelAlias: job.Action.Name, ecs.LabelTaskRole: appRole,
	}
	if cfg.Scale != nil && cfg.Scale.Min > 0 {
		labels[ecs.LabelDesired] = strconv.Itoa(cfg.Scale.Min)
	}
	if cfg.Size != nil {
		labels[ecs.LabelCPU] = strconv.Itoa(int(cfg.Size.CPU * 1024))
	}
	switch {
	case cfg.Public && cfg.Port > 0:
		job.Progress(StepDeploying, "Setting up the load balancer")
		tg, listener, lbURL, xerr := expose(ctx, s, job, net, lbGroup, envName)
		if xerr != nil {
			return "", "", xerr
		}
		labels[ecs.LabelTargetGroup], labels[ecs.LabelListener] = tg, listener
		url = lbURL
	case cfg.Internal && cfg.Port > 0:
		job.Progress(StepDeploying, "Setting up the internal load balancer")
		tg, listener, lbURL, xerr := exposeInternally(ctx, s, job, net, taskGroup, envName)
		if xerr != nil {
			return "", "", xerr
		}
		labels[ecs.LabelTargetGroup], labels[ecs.LabelListener] = tg, listener
		url = lbURL
	}
	for _, key := range sortedKeys(job.Secrets) {
		arn, serr := s.PutSecret(ctx, "loomplane/"+instanceSlug(job)+"/"+key, job.Env[key])
		if serr != nil {
			return "", "", serr
		}
		labels[ecs.LabelSecret+key] = arn
	}
	env := map[string]string{}
	for k, v := range job.Env {
		if !job.Secrets[k] {
			env[k] = v
		}
	}
	spec := cpprovider.ServiceSpec{Name: mainService, Image: image, Env: env}
	if cfg.Port > 0 {
		spec.Ports = []cpprovider.PortSpec{{Container: cfg.Port, Protocol: "http"}}
	}
	if cfg.Size != nil {
		spec.Resources.MemoryMB = cfg.Size.Memory
	}

	key := e.provider(job)
	ctx = infrastructure.WithPrincipal(ctx, principal(job))
	inst, name, err := currentInstance(ctx, e.Ctrl, job, key)
	if err != nil {
		return "", "", err
	}
	if inst == nil {
		created, err := e.Ctrl.Instances.Create(ctx, instance.CreateRequest{
			Name: name, ProviderName: key, Region: s.Region(),
			Services: []cpprovider.ServiceSpec{spec}, Labels: labels,
		})
		if err != nil {
			return "", "", fmt.Errorf("create the ecs service: %w", err)
		}
		return created.ID.String(), url, nil
	}
	if _, err := e.Ctrl.Instances.Update(ctx, inst.ID, instance.UpdateRequest{Services: []cpprovider.ServiceSpec{spec}, Labels: labels}); err != nil {
		return "", "", fmt.Errorf("update the ecs service: %w", err)
	}
	if _, err := e.Ctrl.Deploys.Deploy(ctx, deploy.DeployRequest{
		InstanceID: inst.ID, Services: []cpprovider.ServiceDeploySpec{{Name: mainService, Image: image, Env: env}},
		CommitSHA: job.CommitSHA, Notes: "Plan " + job.Plan.ID,
	}); err != nil {
		return "", "", fmt.Errorf("deploy to ecs: %w", err)
	}
	return inst.ID.String(), url, nil
}

func listenerURL(dns string, l *aws.Listener) string {
	scheme, port := "http", l.Port
	if l.HTTPS {
		scheme = "https"
	}
	if (scheme == "http" && port == 80) || (scheme == "https" && port == 443) {
		return scheme + "://" + dns
	}
	return scheme + "://" + dns + ":" + strconv.Itoa(int(port))
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k, on := range m {
		if on {
			out = append(out, k)
		}
	}
	slices.Sort(out)
	return out
}

// provider registers the connection's ECS provider for its region, once,
// and returns its key.
func (e *ECS) provider(job *Job) string {
	region := job.Connection.Details["region"]
	key := ecs.Key(job.Connection.ID, region)
	if _, err := e.Ctrl.Providers.Get(key); err == nil {
		return key
	}
	orgID, conn := job.Deployment.OrgID, job.Connection
	session := func(ctx context.Context) (*aws.Session, error) {
		c, cred, err := e.Credentials.Credential(ctx, orgID, conn.ID)
		if err != nil {
			return nil, err
		}
		k, err := aws.ParseKey(cred)
		if err != nil {
			return nil, err
		}
		return e.AWS.Session(k, c.Details["region"], c.Account), nil
	}
	e.Ctrl.Providers.Register(key, ecs.New(conn.ID, region, session, e.Ctrl.Instances))
	return key
}

// Remove implements Executor: Ctrl Plane deletes the instance, and the
// provider the ECS service, its listener and target group.
func (e *ECS) Remove(ctx context.Context, job *Job, b *planning.Binding) error {
	if b.ExternalID == "" {
		return nil
	}
	iid, err := id.Parse(b.ExternalID)
	if err != nil {
		return fmt.Errorf("the binding names no instance: %w", err)
	}
	e.provider(job)
	return e.Ctrl.Instances.Delete(infrastructure.WithPrincipal(ctx, principal(job)), iid)
}

// Observe implements Executor: the service's image, read from ECS.
func (e *ECS) Observe(ctx context.Context, conn *connections.Connection, credential []byte, b *planning.Binding) (*Observation, error) {
	iid, err := id.Parse(b.ExternalID)
	if err != nil {
		return nil, fmt.Errorf("the binding names no instance: %w", err)
	}
	key, err := aws.ParseKey(credential)
	if err != nil {
		return nil, err
	}
	s := e.AWS.Session(key, conn.Details["region"], conn.Account)
	image, err := s.ServiceImage(ctx, ecsCluster, ecs.ServiceName(iid))
	if err != nil {
		return nil, err
	}
	if image == "" {
		return &Observation{}, nil
	}
	return &Observation{Exists: true, Revision: image}, nil
}
