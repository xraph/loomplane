package deployments

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha1" //nolint:gosec // G505: Vercel names file content by its SHA-1
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/xraph/loomplane/internal/clouds/buildlogs"
	"github.com/xraph/loomplane/internal/clouds/progress"
	"github.com/xraph/loomplane/internal/clouds/vercel"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/planning"
)

const (
	// uploads is how many files go to Vercel at once.
	uploads = 8
	// maxUploadFile is the largest file uploaded; bigger ones are left
	// out of the deployment, since a web app's build doesn't read them.
	maxUploadFile = 32 << 20
)

// vercelFrameworks maps the frameworks analysis names to Vercel's
// presets. Anything else lets Vercel detect it.
var vercelFrameworks = map[string]string{
	"next": "nextjs", "nuxt": "nuxtjs", "remix": "remix", "sveltekit": "sveltekit-1", "astro": "astro",
	"vite": "vite", "vitepress": "vitepress", "gatsby": "gatsby",
}

// Vercel deploys web apps and static sites as Vercel deployments: the
// commit's files uploaded, built by Vercel, and served from its network.
// It's a Loomplane capability adapter, not a Ctrl Plane provider: a
// Vercel deployment is an immutable build behind a URL, not an instance
// that starts and stops.
//
// Each service and environment is a Vercel project of its own, deployed
// to its production target, so one environment's variables never reach
// another's.
type Vercel struct {
	Client *vercel.Client
}

var _ Executor = (*Vercel)(nil)

type vercelState struct {
	ProjectID    string `json:"project_id,omitempty"`
	DeploymentID string `json:"deployment_id,omitempty"`
	LogAttempt   string `json:"log_attempt,omitempty"`
}

func token(job *Job) vercel.Token {
	tok := vercel.Token{Value: strings.TrimSpace(string(job.Credential))}
	if job.Connection != nil {
		tok.TeamID = job.Connection.Details["team_id"]
	}
	return tok
}

// ProjectName is the Vercel project a service runs as in an environment.
func ProjectName(job *Job) string {
	return strings.ToLower(job.Action.Name + "-" + job.Deployment.Environment + "-" + idSuffix(job.Deployment.ProjectID))
}

// Apply implements Executor.
func (v *Vercel) Apply(ctx context.Context, job *Job) (*Outcome, error) {
	cfg := job.Action.Service
	if cfg == nil {
		return nil, fmt.Errorf("%w: vercel runs web apps and static sites, not %s", ErrNoExecutor, job.Action.Subject)
	}
	tok := token(job)
	var state vercelState
	if len(job.Resume) > 0 {
		_ = json.Unmarshal(job.Resume, &state) //nolint:errcheck // an unreadable checkpoint starts over
	}
	save := func() {
		data, _ := json.Marshal(state) //nolint:errcheck // a plain struct always encodes
		_ = job.Checkpoint(data)       //nolint:errcheck // the next heartbeat retries it
	}
	ctx = progress.With(ctx, func(m string) {
		job.Progress(StepBuilding, m)
		save()
	})

	settings := vercel.ProjectSettings{}
	if f, ok := vercelFrameworks[cfg.Framework]; ok {
		settings.Framework = &f
	}
	if root := strings.Trim(cfg.Source, "/"); root != "" && root != "." {
		settings.RootDirectory = &root
	}
	job.Progress(StepDeploying, "Setting up the Vercel project")
	project, err := v.Client.EnsureProject(ctx, tok, ProjectName(job), settings)
	if err != nil {
		return nil, err
	}
	state.ProjectID = project.ID
	if checkpointErr := checkpointCloudIdentity(job, state); checkpointErr != nil {
		return nil, checkpointErr
	}
	if serr := v.Client.SetEnv(ctx, tok, project.ID, job.Env, job.Secrets); serr != nil {
		return nil, serr
	}
	if state.DeploymentID == "" {
		id, derr := v.deploy(ctx, tok, job, project)
		if derr != nil {
			return nil, derr
		}
		state.DeploymentID = id
		save()
	}
	job.Progress(StepBuilding, "Building on Vercel")
	if state.LogAttempt == "" {
		state.LogAttempt = job.LogAttempt()
		save()
	}
	output := startManagedOutput(ctx, job, state.DeploymentID, state.LogAttempt, func(ctx context.Context, cursor string, follow bool, emit buildlogs.Emit) (string, bool, error) {
		return v.Client.BuildLogs(ctx, tok, state.DeploymentID, cursor, follow, emit)
	})
	d, err := v.Client.WaitDeployment(ctx, tok, state.DeploymentID)
	output.finish(ctx, d != nil)
	if err != nil {
		return nil, err
	}
	return &Outcome{ExternalID: project.ID, URL: d.Address(), Revision: d.ID}, nil
}

// deploy uploads the commit and starts a production deployment of it.
func (v *Vercel) deploy(ctx context.Context, tok vercel.Token, job *Job, project *vercel.Project) (string, error) {
	job.Progress(StepDeploying, "Uploading the source to Vercel")
	files, err := v.upload(ctx, tok, job)
	if err != nil {
		return "", err
	}
	d, err := v.Client.CreateDeployment(ctx, tok, project, files, map[string]string{
		"githubCommitSha": job.CommitSHA, "loomplanePlan": job.Plan.ID,
	})
	if err != nil {
		return "", fmt.Errorf("create the vercel deployment: %w", err)
	}
	return d.ID, nil
}

// upload sends every file of the commit to Vercel, a few at a time, and
// lists them, relative to the repository's root. The whole repository
// goes, not just the app's directory, so a workspace's shared packages
// are there when Vercel builds.
func (v *Vercel) upload(ctx context.Context, tok vercel.Token, job *Job) ([]File, error) {
	src, _, err := job.Source(ctx)
	if err != nil {
		return nil, err
	}
	defer src.Close() // read-only
	zr, err := gzip.NewReader(src)
	if err != nil {
		return nil, fmt.Errorf("read the source: %w", err)
	}
	defer zr.Close() // read-only
	tr := tar.NewReader(zr)

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	type upload struct {
		sha  string
		data []byte
	}
	work := make(chan upload)
	var wg sync.WaitGroup
	var once sync.Once
	var failed error
	for range uploads {
		wg.Go(func() {
			for u := range work {
				if err := v.Client.UploadFile(ctx, tok, u.sha, u.data); err != nil {
					once.Do(func() { failed = err; cancel() })
				}
			}
		})
	}
	var files []File
	seen := map[string]bool{}
	var readErr error
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			readErr = fmt.Errorf("read the source: %w", err)
			break
		}
		if h.Typeflag != tar.TypeReg || h.Size > maxUploadFile {
			continue
		}
		_, rel, ok := strings.Cut(strings.TrimPrefix(h.Name, "./"), "/")
		if !ok || rel == "" {
			continue
		}
		data, err := io.ReadAll(tr)
		if err != nil {
			readErr = fmt.Errorf("read the source: %w", err)
			break
		}
		sum := sha1.Sum(data) //nolint:gosec // G401: Vercel names file content by its SHA-1
		sha := hex.EncodeToString(sum[:])
		files = append(files, File{File: rel, SHA: sha, Size: int64(len(data))})
		if seen[sha] {
			continue
		}
		seen[sha] = true
		select {
		case work <- upload{sha: sha, data: data}:
		case <-ctx.Done():
		}
		if ctx.Err() != nil {
			break
		}
	}
	close(work)
	wg.Wait()
	switch {
	case failed != nil:
		return nil, fmt.Errorf("upload to vercel: %w", failed)
	case readErr != nil:
		return nil, readErr
	}
	return files, nil
}

// File is one file of a deployment.
type File = vercel.File

// Remove implements Executor: it deletes the Vercel project, and with it
// its deployments.
func (v *Vercel) Remove(ctx context.Context, job *Job, b *planning.Binding) error {
	if b.ExternalID == "" {
		return nil
	}
	return v.Client.DeleteProject(ctx, token(job), b.ExternalID)
}

// Observe implements Executor: the project, and which deployment its
// production domain serves.
func (v *Vercel) Observe(ctx context.Context, conn *connections.Connection, credential []byte, b *planning.Binding) (*Observation, error) {
	tok := vercel.Token{Value: strings.TrimSpace(string(credential))}
	if conn != nil {
		tok.TeamID = conn.Details["team_id"]
	}
	p, err := v.Client.GetProject(ctx, tok, b.ExternalID)
	if vercel.IsNotFound(err) {
		return &Observation{}, nil
	}
	if err != nil {
		return nil, err
	}
	return &Observation{Exists: true, Revision: p.Production()}, nil
}
