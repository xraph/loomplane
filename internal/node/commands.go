package node

import (
	"bufio"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"os/user"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/xraph/forge/cli"

	"github.com/xraph/loomplane/pkg/client"
	"github.com/xraph/loomplane/pkg/nodeproto"
)

// Environment variables the node commands read when a flag is not given.
const (
	envURL      = "LOOMPLANE_URL"
	envKey      = "LOOMPLANE_API_KEY"
	envStateDir = "LOOMPLANE_NODE_STATE"
	// envToken is a join token, so it stays out of the process's arguments,
	// where any user on the machine can read it. node.sh passes it through
	// sudo with --preserve-env.
	envToken = "LOOMPLANE_JOIN_TOKEN"
)

// labelBuildWorkers is the label --builder dedicated puts on a node.
const labelBuildWorkers = "build_workers"

// Options configure the node command group. Both binaries mount it: loomctl
// with a Login from its saved settings, loomplane with none.
type Options struct {
	Version string
	// Out receives what the commands print. Nil means os.Stdout.
	Out io.Writer
	// Login returns where Loomplane is and the API key to use. Nil means
	// the commands take --url and --key, or the environment.
	Login func() (url, key string, err error)
	// Getenv reads the environment. Nil means os.Getenv.
	Getenv func(string) string

	// Seams for tests.
	goos    string
	euid    func() int
	home    string
	svc     ServiceManager
	detect  DetectOptions
	binary  func() (string, error)
	chown   func(path string, uid, gid int) error
	runNode func(ctx context.Context, st State, o RuntimeOptions) error
	// systemBin is where a Linux service's binary lives, root's and out of
	// the service user's reach. Empty means /usr/local/lib/loomplane-node.
	systemBin string
	// in and isTerminal drive the endpoint question of node join.
	in         io.Reader
	isTerminal func() bool
	// serviceReady checks that this system can host the service.
	serviceReady func() error
	// releaseBase, goarch, httpc and verifyRelease are where node update
	// finds releases, the architecture it asks for, how it downloads and how
	// it checks the signature.
	releaseBase   string
	goarch        string
	httpc         *http.Client
	verifyRelease func(ctx context.Context, version string, checksums, bundle []byte, artifact, sum string) error
	// settle is how long node update watches the service after starting it
	// on a new binary. Zero means updateSettle; tests make it short.
	settle time.Duration
}

type cmds struct {
	opts Options
}

// Commands returns the node command group: a single `node` command. None of
// its handlers builds the Forge app, so a machine with no database runs them.
func Commands(opts Options) []cli.Command {
	c := &cmds{opts: opts}
	o := &c.opts
	if o.Out == nil {
		o.Out = os.Stdout
	}
	if o.Getenv == nil {
		o.Getenv = os.Getenv
	}
	if o.goos == "" {
		o.goos = goosNow()
	}
	if o.euid == nil {
		o.euid = os.Geteuid
	}
	if o.home == "" {
		o.home, _ = os.UserHomeDir() //nolint:errcheck // only macOS needs it, and fails later with a clear path
	}
	if o.svc == nil {
		o.svc = defaultManager(o.goos, o.home, o.euid())
	}
	if o.binary == nil {
		o.binary = os.Executable
	}
	if o.chown == nil {
		o.chown = os.Lchown
	}
	if o.in == nil {
		o.in = os.Stdin
	}
	if o.isTerminal == nil {
		o.isTerminal = stdinIsTerminal
	}
	if o.serviceReady == nil {
		o.serviceReady = func() error { return systemReady(o.goos) }
	}
	if o.runNode == nil {
		o.runNode = c.runForeground
	}
	if o.releaseBase == "" {
		o.releaseBase = releaseBase
	}
	if o.goarch == "" {
		o.goarch = runtime.GOARCH
	}
	if o.systemBin == "" {
		o.systemBin = systemBinDir
	}
	if o.settle == 0 {
		o.settle = updateSettle
	}

	stateFlag := cli.NewStringFlag("state-dir", "", "Where the node keeps its identity and config (default: "+DefaultStateDir(o.goos, o.home)+")", "")
	jsonFlag := cli.NewBoolFlag("json", "", "Print JSON, for scripts", false)
	urlFlag := cli.NewStringFlag("url", "", "Loomplane's address", "")
	keyFlag := cli.NewStringFlag("key", "", "An API key, to act as you; "+envKey+" works too", "")
	return []cli.Command{cli.NewCommand("node", "Join this machine to Loomplane as a node", nil,
		cli.WithSubcommand(cli.NewCommand("join", "Join this machine to Loomplane and run it as a service", c.guard(c.join),
			cli.WithUsage("node join --url https://loomplane.example.com --token lpj_... [--allow docker,dial,probe]"),
			cli.WithFlags(urlFlag,
				cli.NewStringFlag("token", "", "A join token ("+envToken+" works too, and keeps it out of the process list); without one, node join mints one with your login or --key", ""),
				keyFlag,
				cli.NewStringFlag("name", "", "The node's name; the server picks the hostname if empty", ""),
				cli.NewStringSliceFlag("label", "", "A label, as key=value; repeat it for each", nil),
				cli.NewStringSliceFlag("allow", "", "What Loomplane may use here: docker, dial, probe, kube or firecracker", nil),
				cli.NewStringFlag("cloud", "", "The cloud account this node joins", ""),
				cli.NewStringFlag("builder", "", "dedicated makes this node a dedicated build worker", ""),
				cli.NewBoolFlag("edge", "", "Make this node an edge, which serves traffic", false),
				cli.NewBoolFlag("foreground", "", "Run in this terminal instead of installing a service", false),
				stateFlag))),
		cli.WithSubcommand(cli.NewCommand("run", "Run the node in the foreground; the service and the container image use this", c.guard(c.run),
			cli.WithFlags(stateFlag,
				cli.NewBoolFlag("root-allowlist", "", "Trust only a root-owned allow.json, and take allow only from root; the Linux service sets it", false)))),
		cli.WithSubcommand(cli.NewCommand("status", "Show this node's config, key fingerprint, endpoints and whether it runs", c.guard(c.status),
			cli.WithFlags(stateFlag, jsonFlag))),
		cli.WithSubcommand(cli.NewCommand("leave", "Take this machine out of Loomplane: stop the service and remove the config", c.guard(c.leave),
			cli.WithFlags(stateFlag, cli.NewBoolFlag("purge", "", "Also remove what Loomplane put on this machine (nothing yet)", false)))),
		cli.WithSubcommand(cli.NewCommand("allow", "Widen what Loomplane may use on this machine", c.guard(c.allow),
			cli.WithUsage("node allow docker [dial probe kube firecracker]"),
			cli.WithFlag(stateFlag))),
		cli.WithSubcommand(cli.NewCommand("update", "Replace the node's binary with a signed release and restart the service", c.guard(c.update),
			cli.WithUsage("node update --release v1.9.0"),
			cli.WithFlags(stateFlag, cli.NewStringFlag("release", "", "The release to install, such as v1.9.0", "")))),
		cli.WithSubcommand(cli.NewCommand("verify-release", "Check a downloaded release against its signature", c.guard(c.verifyRelease),
			cli.WithUsage("node verify-release --release v1.9.0 --archive loomctl_1.9.0_linux_amd64.tar.gz --checksums binaries-checksums.txt --bundle binaries-checksums.txt.sigstore.json"),
			cli.WithFlags(
				cli.NewStringFlag("release", "", "The release the files belong to", ""),
				cli.NewStringFlag("archive", "", "The downloaded release archive", ""),
				cli.NewStringFlag("checksums", "", "binaries-checksums.txt", ""),
				cli.NewStringFlag("bundle", "", "binaries-checksums.txt.sigstore.json", "")))),
		cli.WithSubcommand(cli.NewCommand("list", "List the organization's nodes", c.guard(c.list),
			cli.WithFlags(urlFlag, keyFlag, jsonFlag))),
		cli.WithSubcommand(cli.NewCommand("revoke", "Stop trusting a node, by name or id", c.guard(c.revoke),
			cli.WithUsage("node revoke <name-or-id>"),
			cli.WithFlags(urlFlag, keyFlag))),
	)}
}

// guard refuses a system the node does not run on, and turns a revoked node's
// error into one that carries its exit code (77 for a revoked node) through Forge.
func (c *cmds) guard(h func(cli.CommandContext) error) cli.CommandHandler {
	return func(ctx cli.CommandContext) error {
		if err := supportedFor(c.opts.goos); err != nil {
			return err
		}
		return exitError(h(ctx))
	}
}

// exitError wraps err in the error Forge reads an exit code from, when the
// node has a code of its own for it. Other errors exit with 1, as before.
func exitError(err error) error {
	if code := ExitCode(err); code != 0 && code != 1 {
		return cli.WrapError(err, "node", code)
	}
	return err
}

func (c *cmds) printf(format string, a ...any) {
	_, _ = fmt.Fprintf(c.opts.Out, format, a...)
}

// stateDir is the absolute state directory. A relative one is refused: a
// command run as root must know exactly which directory it changes.
func (c *cmds) stateDir(ctx cli.CommandContext) (string, error) {
	d := ctx.String("state-dir")
	if d == "" {
		d = c.opts.Getenv(envStateDir)
	}
	if d == "" {
		d = DefaultStateDir(c.opts.goos, c.opts.home)
	}
	if !filepath.IsAbs(d) {
		return "", fmt.Errorf("--state-dir %q must be an absolute path", d)
	}
	return filepath.Clean(d), nil
}

// stateOwner is the uid that owns dir, when root works on a state dir that
// belongs to the service user. It returns -1 in any other case.
func (c *cmds) stateOwner(dir string) int {
	if c.opts.euid() != 0 {
		return -1
	}
	fi, err := os.Lstat(dir)
	if err != nil {
		return -1
	}
	if owner, ok := fileOwner(fi); ok && owner != 0 {
		return int(owner)
	}
	return -1
}

// stateFor is the State for dir. When root works on the service user's state
// dir, it answers the ownership checks as that user, so root can read and
// change what the service owns; the caller hands the files back with
// handBack.
func (c *cmds) stateFor(dir string) State {
	st := State{Dir: dir}
	if owner := c.stateOwner(dir); owner >= 0 {
		st.euid = func() int { return owner }
	}
	return st
}

// handBack gives root's new files in the state dir to the service user.
func (c *cmds) handBack(dir string, names ...string) error {
	owner := c.stateOwner(dir)
	if owner < 0 {
		return nil
	}
	for _, n := range names {
		if err := c.opts.chown(filepath.Join(dir, n), owner, -1); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	return nil
}

// binDir is where the service's copy of loomctl lives: a root-owned system
// directory on Linux, so only root's node update replaces it, and the state
// dir's bin on macOS, where the node runs as you anyway.
func (c *cmds) binDir(stateDir string) string {
	if c.opts.goos == "linux" {
		return c.opts.systemBin
	}
	return filepath.Join(stateDir, "bin")
}

// ceilingTrust is whose allow.json the commands take for dir: their own, and
// the state dir owner's when that is a person running the node as
// themselves, not the service user.
func (c *cmds) ceilingTrust(dir string) func(uint32) bool {
	me := uint32(c.opts.euid()) //nolint:gosec // a uid is never negative
	owner := c.stateOwner(dir)
	person := owner >= 0 && !isServiceUser(owner)
	return func(uid uint32) bool { return uid == me || (person && int64(uid) == int64(owner)) }
}

// isServiceUser reports whether uid is the user the Linux service runs as.
func isServiceUser(uid int) bool {
	u, err := user.LookupId(strconv.Itoa(uid))
	return err == nil && u.Username == serviceUser
}

func (c *cmds) needRoot(what string) error {
	if c.opts.goos == "linux" && c.opts.euid() != 0 {
		return fmt.Errorf("%s needs root on Linux, because the node's files belong to its service user: run it with sudo", what)
	}
	return nil
}

// credentials finds where Loomplane is and the key to act with: the flags,
// then Login, then the environment. A missing key is returned empty, since a
// join token can stand in for it.
func (c *cmds) credentials(ctx cli.CommandContext) (url, key string, err error) {
	url, key = ctx.String("url"), ctx.String("key")
	if c.opts.Login != nil && (url == "" || key == "") {
		lu, lk, lerr := c.opts.Login()
		if lerr == nil {
			// A key belongs to the Loomplane it was saved for.
			if url == "" {
				url = lu
			}
			if key == "" && url == lu {
				key = lk
			}
		} else if url == "" {
			err = lerr
		}
	}
	if url == "" {
		url = c.opts.Getenv(envURL)
	}
	if key == "" && (ctx.String("url") == "" || ctx.String("url") == c.opts.Getenv(envURL)) {
		key = c.opts.Getenv(envKey)
	}
	if url == "" {
		if err != nil {
			return "", "", err
		}
		return "", "", fmt.Errorf("say where Loomplane is with --url, or set %s", envURL)
	}
	return url, key, nil
}

// apiClient is a client for list and revoke, which act as you.
func (c *cmds) apiClient(ctx cli.CommandContext) (*client.Client, error) {
	url, key, err := c.credentials(ctx)
	if err != nil {
		return nil, err
	}
	if key == "" {
		return nil, fmt.Errorf("give an API key with --key or %s, along with --url", envKey)
	}
	return client.New(url, key), nil
}

func parseAllow(items []string) ([]nodeproto.Endpoint, error) {
	var eps []nodeproto.Endpoint
	for _, it := range items {
		for _, p := range strings.Split(it, ",") {
			if p = strings.TrimSpace(p); p != "" {
				eps = append(eps, nodeproto.Endpoint(p))
			}
		}
	}
	return AllowableOnly(eps)
}

func parseLabels(items []string) (map[string]string, error) {
	labels := map[string]string{}
	for _, it := range items {
		k, v, ok := strings.Cut(it, "=")
		if k = strings.TrimSpace(k); !ok || k == "" {
			return nil, fmt.Errorf("--label %q: write it as key=value", it)
		}
		labels[k] = strings.TrimSpace(v)
	}
	return labels, nil
}

func (c *cmds) join(ctx cli.CommandContext) error {
	allowFlag := ctx.StringSlice("allow")
	eps, err := parseAllow(allowFlag)
	if err != nil {
		return fmt.Errorf("--allow: %w", err)
	}
	labels, err := parseLabels(ctx.StringSlice("label"))
	if err != nil {
		return err
	}
	switch b := ctx.String("builder"); b {
	case "":
	case "dedicated":
		labels[labelBuildWorkers] = "dedicated"
	default:
		return fmt.Errorf("--builder %q: the only choice is dedicated", b)
	}
	token, cloud, edge, foreground := ctx.String("token"), ctx.String("cloud"), ctx.Bool("edge"), ctx.Bool("foreground")
	if token == "" {
		token = strings.TrimSpace(c.opts.Getenv(envToken))
	}
	if token != "" && (cloud != "" || edge) {
		return errors.New("--cloud and --edge go in the join token, so they cannot be used with a token (--token or " + envToken + "): mint the token with them, or drop the token and use your login")
	}
	dir, err := c.stateDir(ctx)
	if err != nil {
		return err
	}
	// Everything that can fail without the network comes before the token is
	// spent.
	if rerr := c.refuseMacRoot("node join"); rerr != nil {
		return rerr
	}
	if !foreground {
		if rerr := c.needRoot("installing the node as a service"); rerr != nil {
			return fmt.Errorf("%w, or pass --foreground to run it in this terminal", rerr)
		}
		if serr := c.opts.serviceReady(); serr != nil {
			return fmt.Errorf("%w: %w; pass --foreground to run it in this terminal", ErrUnsupported, serr)
		}
	}
	if !foreground {
		bin, berr := c.opts.binary()
		if berr != nil {
			return fmt.Errorf("finding the running binary: %w", berr)
		}
		if uerr := checkUnit(Unit{Binary: bin, StateDir: dir, BinDir: c.binDir(dir)}, c.opts.goos == "linux"); uerr != nil {
			return uerr
		}
	}
	if cerr := checkJoinDir(dir); cerr != nil {
		return cerr
	}
	url, key, err := c.credentials(ctx)
	if err != nil {
		return err
	}
	if token == "" && key == "" {
		return fmt.Errorf("give a join token with --token or %s, or an API key with --key or %s so one can be minted", envToken, envKey)
	}
	// With no --allow on a terminal, show what was found and ask. The answer
	// goes into the minted token's scope too.
	if len(allowFlag) == 0 && c.opts.isTerminal() {
		eps = c.askEndpoints(ctx.Context())
	}
	if token == "" {
		scope := client.NodeJoinScope{Labels: labels, Endpoints: eps, Edge: edge, CloudID: cloud}
		jt, merr := client.New(url, key).CreateNodeJoinToken(ctx.Context(), scope)
		if merr != nil {
			return fmt.Errorf("minting a join token: %w", merr)
		}
		token = jt.Token
	}
	if len(labels) == 0 {
		labels = nil
	}
	if eps == nil {
		eps = []nodeproto.Endpoint{}
	}
	st := c.stateFor(dir)
	cfg, err := Join(ctx.Context(), JoinOptions{URL: url, Token: token, Name: ctx.String("name"), Labels: labels, Endpoints: eps,
		State: st, Out: c.opts.Out, Detect: c.opts.detect})
	if err != nil {
		return err
	}
	if edge {
		cfg.Edge = true
		if err := st.SaveConfig(*cfg); err != nil {
			return err
		}
	}
	if len(cfg.Endpoints) == 0 {
		c.printf("Loomplane may not use anything on this machine yet. Allow something with: node allow docker\n")
	}
	if foreground {
		c.printf("Running in this terminal. Press Ctrl+C to stop.\n")
		return c.opts.runNode(ctx.Context(), State{Dir: dir}, RuntimeOptions{})
	}
	return c.install(ctx.Context(), dir, *cfg)
}

// askEndpoints shows what this machine offers and asks whether Loomplane may
// use it. Anything but yes allows nothing.
func (c *cmds) askEndpoints(ctx context.Context) []nodeproto.Endpoint {
	inv := Detect(ctx, c.opts.detect)
	offered := []nodeproto.Endpoint{nodeproto.EndpointProbe}
	c.printf("\nLoomplane could use this on this machine:\n  %-8s report what the machine has\n", nodeproto.EndpointProbe)
	switch {
	case inv.Docker != nil:
		offered = append(offered, nodeproto.EndpointDocker)
		c.printf("  %-8s run containers with docker %s at %s\n", nodeproto.EndpointDocker, inv.Docker.Version, inv.Docker.Socket)
	case inv.Podman != nil:
		offered = append(offered, nodeproto.EndpointDocker)
		c.printf("  %-8s run containers with podman %s at %s\n", nodeproto.EndpointDocker, inv.Podman.Version, inv.Podman.Socket)
	}
	c.printf("Allow these? [y/N] ")
	line, err := bufio.NewReader(c.opts.in).ReadString('\n')
	if err != nil && line == "" {
		c.printf("\n")
		return []nodeproto.Endpoint{}
	}
	if a := strings.ToLower(strings.TrimSpace(line)); a == "y" || a == "yes" {
		return offered
	}
	return []nodeproto.Endpoint{}
}

// checkJoinDir refuses a state dir that already holds anything but a node's
// own files, so a join can never take over a directory such as /var/lib.
func checkJoinDir(dir string) error {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, e := range entries {
		if !ownStateFile(e.Name()) {
			return fmt.Errorf("%s already holds %q, which is not a node's file; give --state-dir an empty or new directory", dir, e.Name())
		}
		fi, ierr := e.Info() // lstat: a symlink shows as one
		if ierr != nil {
			return ierr
		}
		m := fi.Mode()
		ok := m.IsRegular()
		switch e.Name() {
		case "bin":
			ok = m.IsDir()
		case controlSocket:
			ok = m&fs.ModeSocket != 0
		}
		if !ok {
			return fmt.Errorf("%s holds %q, which is not the kind of file a node keeps there (%s); remove it or use another --state-dir", dir, e.Name(), m.Type())
		}
	}
	return nil
}

// ownStateFile names what a node puts in its state dir: the key, the keys a
// rejoin retired, the config, allow.json, the copied binary's folder, the control
// socket, the macOS log and the temp files an atomic write leaves behind.
func ownStateFile(name string) bool {
	switch name {
	case identityFile, configFile, ceilingFile, "bin", controlSocket, "node.log":
		return true
	}
	return strings.HasPrefix(name, "."+configFile+".tmp-") || strings.HasPrefix(name, "."+identityFile+".tmp-") ||
		strings.HasPrefix(name, "."+ceilingFile+".tmp-") ||
		strings.HasPrefix(name, retiredKeyPrefix)
}

func (c *cmds) refuseMacRoot(what string) error {
	if c.opts.goos == "darwin" && c.opts.euid() == 0 {
		return fmt.Errorf("%s as root would set the node up for root; run it as your own user, without sudo", what)
	}
	return nil
}

// install registers the node as a service and starts it.
func (c *cmds) install(ctx context.Context, dir string, cfg Config) error {
	if c.opts.goos == "darwin" {
		c.printf("macOS nodes are for development. The node runs as a LaunchAgent while you are logged in.\n")
	}
	bin, err := c.opts.binary()
	if err != nil {
		return fmt.Errorf("finding the running binary: %w", err)
	}
	unit := Unit{Binary: bin, StateDir: dir, BinDir: c.binDir(dir), Docker: cfg.DockerSocket != ""}
	if err := c.opts.svc.Install(ctx, unit); err != nil {
		return fmt.Errorf("joined as node %s, but installing the service failed: %w; fix that and run node leave, then join again", cfg.NodeID, err)
	}
	if err := c.opts.svc.Start(ctx); err != nil {
		return fmt.Errorf("joined as node %s, but starting the service failed: %w", cfg.NodeID, err)
	}
	c.printf("The node service is installed and running. Check it with: node status\n")
	return nil
}

func (c *cmds) run(ctx cli.CommandContext) error {
	dir, err := c.stateDir(ctx)
	if err != nil {
		return err
	}
	return c.opts.runNode(ctx.Context(), State{Dir: dir}, RuntimeOptions{RootAllowlist: ctx.Bool("root-allowlist")})
}

// runForeground keeps the connection to Loomplane until it is told to stop,
// revoked or refused as too old.
func (c *cmds) runForeground(ctx context.Context, st State, o RuntimeOptions) error {
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	rt, err := NewRuntimeWith(st, &Registry{}, o)
	if err != nil {
		return err
	}
	rt.Log = slog.New(slog.NewTextHandler(os.Stderr, nil))
	rt.detectBase = c.opts.detect
	return rt.Run(ctx)
}

type statusInfo struct {
	Joined         bool                 `json:"joined"`
	StateDir       string               `json:"state_dir"`
	URL            string               `json:"url,omitempty"`
	NodeID         string               `json:"node_id,omitempty"`
	OrgID          string               `json:"org_id,omitempty"`
	Fingerprint    string               `json:"fingerprint,omitempty"`
	Endpoints      []nodeproto.Endpoint `json:"endpoints"`
	AllowedHere    []nodeproto.Endpoint `json:"allowed_here"`
	PendingAllowed []nodeproto.Endpoint `json:"pending_allowed,omitempty"`
	IngressMode    string               `json:"ingress_mode,omitempty"`
	Edge           bool                 `json:"edge,omitempty"`
	ServiceRunning bool                 `json:"service_running"`
}

func (c *cmds) status(ctx cli.CommandContext) error {
	dir, err := c.stateDir(ctx)
	if err != nil {
		return err
	}
	st := c.stateFor(dir)
	info := statusInfo{StateDir: dir, Endpoints: []nodeproto.Endpoint{}, AllowedHere: []nodeproto.Endpoint{}}
	cfg, err := st.ReadConfig()
	switch {
	case err == nil:
		info.Joined, info.URL, info.NodeID, info.OrgID = true, cfg.URL, cfg.NodeID, cfg.OrgID
		info.PendingAllowed, info.IngressMode, info.Edge = cfg.PendingAllowed, cfg.IngressMode, cfg.Edge
		// What the node serves is what both files allow.
		if ceil, cerr := st.ReadCeiling(c.ceilingTrust(dir)); cerr == nil && ceil.NodeID == cfg.NodeID {
			info.AllowedHere = ceil.Endpoints
		}
		info.Endpoints = nonNil(within(cfg.Endpoints, info.AllowedHere))
		info.PendingAllowed = within(info.PendingAllowed, info.AllowedHere)
		info.Fingerprint = fingerprint(st)
	case errors.Is(err, fs.ErrNotExist):
	case c.opts.goos == "linux" && c.opts.euid() != 0:
		return fmt.Errorf("%w; the node's files belong to its service user, so try again with sudo", err)
	default:
		return err
	}
	info.ServiceRunning, _ = c.opts.svc.Running(ctx.Context()) //nolint:errcheck // a manager that cannot answer means not running
	if ctx.Bool("json") {
		return json.NewEncoder(c.opts.Out).Encode(info)
	}
	if !info.Joined {
		c.printf("Not joined. This machine has not joined a Loomplane (state dir %s).\n", dir)
		return nil
	}
	running := "stopped"
	if info.ServiceRunning {
		running = "running"
	}
	eps := "none"
	if len(info.Endpoints) > 0 {
		eps = joinEndpoints(info.Endpoints, ", ")
	}
	row := func(k, v string) { c.printf("%-13s %s\n", k, v) }
	row("Node", info.NodeID)
	row("Loomplane", info.URL)
	row("Organization", info.OrgID)
	if info.Fingerprint != "" {
		row("Fingerprint", info.Fingerprint)
	}
	row("Endpoints", eps)
	here := "none (sudo loomctl node allow <endpoint> sets it)"
	if len(info.AllowedHere) > 0 {
		here = joinEndpoints(info.AllowedHere, ", ")
	}
	row("Allowed here", here)
	if len(info.PendingAllowed) > 0 {
		row("Pending", joinEndpoints(info.PendingAllowed, ", ")+" (Loomplane has not confirmed these yet)")
	}
	row("Ingress", info.IngressMode)
	if info.Edge {
		row("Role", "edge")
	}
	row("Service", running)
	row("State dir", dir)
	return nil
}

// fingerprint names the node's key, or returns "" when it has none yet. It
// never creates the key.
func fingerprint(st State) string {
	if _, err := os.Stat(filepath.Join(st.Dir, identityFile)); err != nil {
		return ""
	}
	priv, err := st.readKey(filepath.Join(st.Dir, identityFile))
	if err != nil {
		return ""
	}
	pub, ok := priv.Public().(ed25519.PublicKey)
	if !ok {
		return ""
	}
	return nodeproto.Fingerprint(pub)
}

func joinEndpoints(eps []nodeproto.Endpoint, sep string) string {
	s := make([]string, len(eps))
	for i, e := range eps {
		s[i] = string(e)
	}
	return strings.Join(s, sep)
}

func (c *cmds) allow(ctx cli.CommandContext) error {
	if err := c.needRoot("node allow"); err != nil {
		return err
	}
	if ctx.NArgs() == 0 {
		return fmt.Errorf("say what to allow, from %v", nodeproto.AllowableEndpoints())
	}
	var asked []nodeproto.Endpoint
	for _, a := range ctx.Args() {
		asked = append(asked, nodeproto.Endpoint(a))
	}
	eps, err := AllowableOnly(asked)
	if err != nil {
		return err
	}
	dir, err := c.stateDir(ctx)
	if err != nil {
		return err
	}
	st := c.stateFor(dir)
	cfg, err := st.ReadConfig()
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return errors.New("this machine has not joined a Loomplane: run node join first")
		}
		return err
	}
	// The ceiling first. It is ours alone (root's, on a Linux service), so a
	// copy that isn't, or is another node's, counts for nothing.
	if err = c.widenCeiling(st, dir, cfg.NodeID, eps); err != nil {
		return err
	}
	sock := filepath.Join(dir, controlSocket)
	list := joinEndpoints(eps, ", ")
	err = controlCall(ctx.Context(), sock, controlRequest{Op: "allow", Endpoints: eps})
	switch {
	case err == nil:
		c.printf("Allowed %s. The running node told Loomplane.\n", list)
		return nil
	case !errors.Is(err, errNoService):
		return err
	}
	// Nothing answers. A service that is running but unreachable must not have
	// its config changed under it, since it would save its own copy over ours.
	if running, _ := c.opts.svc.Running(ctx.Context()); running { //nolint:errcheck // unknown counts as not running
		return fmt.Errorf("the node service is running but its control socket %s does not answer; restart the service, then allow again", sock)
	}
	cfg.Widen(eps)
	if err := st.SaveConfig(cfg); err != nil {
		return err
	}
	if err := c.handBack(dir, configFile); err != nil {
		return err
	}
	c.printf("Allowed %s. The node is not running, so Loomplane learns of it the next time the node starts.\n", list)
	return nil
}

// widenCeiling adds eps to allow.json for node id. It trusts a file owned by
// whoever runs this, and one owned by the state dir's owner when that is a
// person running the node as themselves rather than the service user, the
// same files the node itself trusts. It starts afresh from one it doesn't
// trust. On a Linux service the file stays root's, readable by the service's
// group.
func (c *cmds) widenCeiling(st State, dir, id string, eps []nodeproto.Endpoint) error {
	owner := c.stateOwner(dir)
	cur, err := st.ReadCeiling(c.ceilingTrust(dir))
	switch {
	case err == nil && cur.NodeID == id:
	case errors.Is(err, fs.ErrNotExist):
		cur = Ceiling{}
	default:
		c.printf("allow.json was not this node's own, so it now holds only what you allow here.\n")
		cur = Ceiling{}
	}
	cur.NodeID = id
	for _, e := range eps {
		if !slices.Contains(cur.Endpoints, e) {
			cur.Endpoints = append(cur.Endpoints, e)
		}
	}
	gid := -1
	if owner >= 0 {
		if fi, lerr := os.Lstat(dir); lerr == nil {
			if g, ok := fileGroup(fi); ok {
				gid = int(g)
			}
		}
	}
	return st.SaveCeiling(cur, gid)
}

func (c *cmds) leave(ctx cli.CommandContext) error {
	if err := c.needRoot("node leave"); err != nil {
		return err
	}
	if err := c.refuseMacRoot("node leave"); err != nil {
		return err
	}
	dir, err := c.stateDir(ctx)
	if err != nil {
		return err
	}
	st := c.stateFor(dir)
	joined := true
	if _, err := st.ReadConfig(); err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		joined = false
		c.printf("This machine has not joined a Loomplane.\n")
	}
	if joined {
		note, err := controlCallNote(ctx.Context(), filepath.Join(dir, controlSocket), controlRequest{Op: "leave"})
		switch {
		case err == nil && note != "":
			c.printf("%s.\n", note)
		case err == nil:
			c.printf("Loomplane revoked this node.\n")
		case errors.Is(err, errNoService):
			c.printf("The node is not running, so it cannot tell Loomplane it is leaving: revoke it in the dashboard (Nodes), or with node revoke.\n")
		default:
			// The service runs and is still joined, so keep it and its
			// config until Loomplane has revoked it.
			c.printf("Revoke it in the dashboard (Nodes), or with node revoke, then run node leave again.\n")
			return fmt.Errorf("the running node could not confirm that Loomplane revoked it (%w); its service and config are kept", err)
		}
	}
	// --purge removes what Loomplane put on this machine. Nothing is there
	// until phase 2 adds containers, so it changes nothing yet.
	_ = c.opts.svc.Stop(ctx.Context()) //nolint:errcheck // a revoked node has stopped itself
	if err := c.opts.svc.Remove(ctx.Context()); err != nil && !errors.Is(err, ErrUnsupported) {
		return fmt.Errorf("removing the service: %w", err)
	}
	if joined {
		if err := st.DeleteConfig(); err != nil {
			return err
		}
	}
	c.printf("Removed this node's service and config. Its key stays in %s, and a later join makes a fresh one.\n", dir)
	return nil
}

func (c *cmds) list(ctx cli.CommandContext) error {
	api, err := c.apiClient(ctx)
	if err != nil {
		return err
	}
	nodes, err := api.Nodes(ctx.Context())
	if err != nil {
		return err
	}
	if ctx.Bool("json") {
		if nodes == nil {
			nodes = []client.Node{}
		}
		return json.NewEncoder(c.opts.Out).Encode(nodes)
	}
	tw := tabwriter.NewWriter(c.opts.Out, 0, 8, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "NAME\tSTATUS\tENDPOINTS\tARCH\tVERSION\tLAST SEEN")
	for _, n := range nodes {
		seen := "never"
		if n.LastSeenAt != nil {
			seen = n.LastSeenAt.UTC().Format("2006-01-02 15:04 UTC")
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", n.Name, n.Status, orDash(joinEndpoints(n.Endpoints, ",")), orDash(n.Inventory.Arch), orDash(n.Version), seen)
	}
	return tw.Flush()
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func (c *cmds) revoke(ctx cli.CommandContext) error {
	if ctx.NArgs() != 1 {
		return errors.New("name the node to revoke: node revoke <name-or-id>")
	}
	api, err := c.apiClient(ctx)
	if err != nil {
		return err
	}
	nodes, err := api.Nodes(ctx.Context())
	if err != nil {
		return err
	}
	want := ctx.Arg(0)
	var match []client.Node
	for _, n := range nodes {
		if n.ID == want {
			match = []client.Node{n}
			break
		}
		if n.Name == want {
			match = append(match, n)
		}
	}
	switch len(match) {
	case 0:
		return fmt.Errorf("no node is called or has the id %q; see node list", want)
	case 1:
	default:
		return fmt.Errorf("%d nodes are called %q; revoke one by its id", len(match), want)
	}
	if err := api.RevokeNode(ctx.Context(), match[0].ID); err != nil {
		return err
	}
	c.printf("Revoked %s (%s).\n", match[0].Name, match[0].ID)
	return nil
}
