package node

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"

	"github.com/xraph/forge/cli"

	"github.com/xraph/loomplane/pkg/client"
	"github.com/xraph/loomplane/pkg/nodeproto"
)

// stoppedFile marks a node or runner its owner stopped, so status tells a
// stop from a crash. Start removes it.
const stoppedFile = "stopped"

// envRunnerStateDir overrides a runner's state directory.
const envRunnerStateDir = "LOOMPLANE_RUNNER_STATE"

// runnerEndpoints are all a runner serves besides control.
var runnerEndpoints = []nodeproto.Endpoint{nodeproto.EndpointProbe, nodeproto.EndpointFirecracker, nodeproto.EndpointRelay}

// runnerCommand is the runner command group. A runner is a node with the
// runner role: it builds every image in a fresh Firecracker microVM and
// relays deploys to hosts only it can reach. It needs Linux, root and KVM.
func (c *cmds) runnerCommand() cli.Command {
	stateFlag := cli.NewStringFlag("state-dir", "", "Where the runner keeps its identity and config (default: "+DefaultRunnerStateDir+")", "")
	jsonFlag := cli.NewBoolFlag("json", "", "Print JSON, for scripts", false)
	return cli.NewCommand("runner", "Turn this machine into a runner that builds in Firecracker microVMs and relays deploys", nil,
		cli.WithSubcommand(cli.NewCommand("start", "Join this machine as a runner if it hasn't, and start it; it survives reboots until runner stop", c.guard(c.runnerStart),
			cli.WithUsage("runner start --url https://loomplane.example.com --token lpj_... [--relay-allow 10.0.0.0/16] [--once]"),
			cli.WithFlags(
				cli.NewStringFlag("url", "", "Loomplane's address", ""),
				cli.NewStringFlag("token", "", "A runner's join token from its pool ("+envToken+" works too); without one, --key and --pool mint one", ""),
				cli.NewStringFlag("key", "", "An API key, to mint a join token as you; "+envKey+" works too", ""),
				cli.NewStringFlag("pool", "", "The runner pool to join, when minting a token", ""),
				cli.NewStringFlag("name", "", "The runner's name; Loomplane picks the hostname if empty", ""),
				cli.NewStringFlag("firecracker-config", "", "The Firecracker host config to build with (default: "+DefaultRunnerFirecrackerConfig+")", ""),
				cli.NewStringSliceFlag("relay-allow", "", "A network deploys may be relayed to, as a CIDR; repeat it for each", nil),
				cli.NewBoolFlag("ephemeral", "", "This runner was started for builds and is removed after them", false),
				cli.NewStringFlag("toolchain", "", "An https URL of a Firecracker toolchain bundle to install first", ""),
				cli.NewStringFlag("toolchain-sha256", "", "The toolchain bundle's sha256, checked before anything is unpacked", ""),
				cli.NewBoolFlag("once", "", "Run in this terminal until stopped, and install nothing", false),
				stateFlag))),
		cli.WithSubcommand(cli.NewCommand("stop", "Stop the runner and keep it stopped across reboots, until runner start", c.guard(c.runnerStop),
			cli.WithFlags(stateFlag))),
		cli.WithSubcommand(cli.NewCommand("status", "Show the runner's pool, config, KVM check and whether it runs", c.guard(c.runnerStatus),
			cli.WithFlags(stateFlag, jsonFlag))),
		cli.WithSubcommand(cli.NewCommand("leave", "Take this machine out of Loomplane: stop the runner and remove its service and config", c.guard(c.runnerLeave),
			cli.WithFlags(stateFlag))),
		cli.WithSubcommand(cli.NewCommand("run", "Run the runner in the foreground; its service uses this", c.guard(c.runnerRun),
			cli.WithFlags(stateFlag))),
		cli.WithSubcommand(cli.NewCommand("relay", "The networks this runner may relay deploys to", nil,
			cli.WithSubcommand(cli.NewCommand("allow", "Let this runner relay deploys to more networks", c.guard(c.runnerRelayAllow),
				cli.WithUsage("runner relay allow 10.0.0.0/16 [192.168.1.0/24 ...]"),
				cli.WithFlags(stateFlag))))),
	)
}

// runnerDir is the runner's absolute state directory.
func (c *cmds) runnerDir(ctx cli.CommandContext) (string, error) {
	d := ctx.String("state-dir")
	if d == "" {
		d = c.opts.Getenv(envRunnerStateDir)
	}
	if d == "" {
		d = DefaultRunnerStateDir
	}
	if !filepath.IsAbs(d) {
		return "", fmt.Errorf("--state-dir %q must be an absolute path", d)
	}
	return filepath.Clean(d), nil
}

// needRunnerHost refuses a machine that can't be a runner: not Linux, not
// root, or no KVM. It runs before a token is spent.
func (c *cmds) needRunnerHost() error {
	if c.opts.goos != "linux" {
		return fmt.Errorf("%w: a runner builds in Firecracker microVMs, which need Linux with KVM", ErrUnsupported)
	}
	if c.opts.euid() != 0 {
		return errors.New("a runner needs root, for KVM, the jailer and its network: run it with sudo")
	}
	return c.opts.kvm()
}

// kvmReady checks /dev/kvm is a character device.
func kvmReady() error {
	fi, err := os.Stat("/dev/kvm")
	if err != nil || fi.Mode()&fs.ModeCharDevice == 0 {
		return errors.New("/dev/kvm isn't here, so this machine can't run microVMs; use bare metal, or a VM with nested virtualization on")
	}
	return nil
}

func (c *cmds) runnerStart(ctx cli.CommandContext) error {
	if err := c.needRunnerHost(); err != nil {
		return err
	}
	dir, err := c.runnerDir(ctx)
	if err != nil {
		return err
	}
	// One machine, one role: a node serves production, and a runner runs
	// untrusted builds beside it.
	if node, nerr := (State{Dir: DefaultStateDir(c.opts.goos, c.opts.home)}).LoadConfig(); nerr == nil && node.Role != RoleRunner {
		return fmt.Errorf("this machine is already node %s; a machine is a node or a runner, never both. Run node leave first, or use another machine", node.NodeID)
	}
	st := State{Dir: dir}
	once := ctx.Bool("once")
	cfg, lerr := st.LoadConfig()
	switch {
	case lerr == nil && cfg.Role != RoleRunner:
		return fmt.Errorf("%s holds node %s, not a runner; use another --state-dir", dir, cfg.NodeID)
	case lerr == nil:
		_ = os.Remove(filepath.Join(dir, stoppedFile))
		if once {
			c.printf("Running runner %s in this terminal. Press Ctrl+C to stop.\n", cfg.NodeID)
			return c.opts.runRunner(ctx.Context(), st)
		}
		return c.installRunner(ctx.Context(), dir, cfg)
	case !errors.Is(lerr, fs.ErrNotExist):
		return lerr
	}
	fcConfig := ctx.String("firecracker-config")
	if fcConfig == "" {
		fcConfig = DefaultRunnerFirecrackerConfig
	}
	if !filepath.IsAbs(fcConfig) {
		return fmt.Errorf("--firecracker-config %q must be an absolute path", fcConfig)
	}
	if bundle := ctx.String("toolchain"); bundle != "" {
		c.printf("Installing the Firecracker toolchain from %s\n", bundle)
		if _, terr := InstallToolchain(ctx.Context(), ToolchainOptions{URL: bundle, SHA256: ctx.String("toolchain-sha256"),
			Root: c.opts.toolchainRoot, Config: fcConfig, StateDir: filepath.Join(dir, "firecracker"), RuntimeDir: "/srv/loomplane-runner",
			Inventory: Detect(ctx.Context(), c.opts.detect), HTTP: c.opts.toolchainHTTP}); terr != nil {
			return terr
		}
	}
	if _, serr := os.Stat(fcConfig); serr != nil {
		return fmt.Errorf("the Firecracker host config %s: %w; install the runner's Firecracker release, or point --firecracker-config at its config", fcConfig, serr)
	}
	relay := ctx.StringSlice("relay-allow")
	if len(ParseRelayAllow(relay)) != len(relay) {
		return errors.New("--relay-allow takes CIDR prefixes such as 10.0.0.0/16")
	}
	if cerr := checkJoinDir(dir); cerr != nil {
		return cerr
	}
	token := ctx.String("token")
	if token == "" {
		token = strings.TrimSpace(c.opts.Getenv(envToken))
	}
	url, key, err := c.credentials(ctx)
	if err != nil {
		return err
	}
	if token == "" {
		pool := ctx.String("pool")
		if key == "" || pool == "" {
			return fmt.Errorf("give a runner's join token with --token or %s, or --key and --pool so one can be minted", envToken)
		}
		jt, merr := client.New(url, key).CreateNodeJoinToken(ctx.Context(), client.NodeJoinScope{Role: RoleRunner, PoolID: pool, Endpoints: runnerEndpoints})
		if merr != nil {
			return fmt.Errorf("minting a runner's join token: %w", merr)
		}
		token = jt.Token
	}
	joined, err := Join(ctx.Context(), JoinOptions{URL: url, Token: token, Name: ctx.String("name"), Endpoints: slices.Clone(runnerEndpoints),
		State: st, Out: c.opts.Out, Detect: c.opts.detect,
		Runner: &RunnerJoin{FirecrackerConfig: fcConfig, Relay: relay, Ephemeral: ctx.Bool("ephemeral")}})
	if err != nil {
		return err
	}
	c.printf("Joined pool %s as runner %s.\n", joined.PoolID, joined.NodeID)
	if once {
		c.printf("Running in this terminal. Press Ctrl+C to stop.\n")
		return c.opts.runRunner(ctx.Context(), st)
	}
	return c.installRunner(ctx.Context(), dir, *joined)
}

// installRunner installs the runner's service, enables it so it starts at
// boot, and starts it.
func (c *cmds) installRunner(ctx context.Context, dir string, cfg Config) error {
	bin, err := c.opts.binary()
	if err != nil {
		return fmt.Errorf("finding the running binary: %w", err)
	}
	if err = c.opts.runnerSvc.Install(ctx, bin, dir); err != nil {
		return fmt.Errorf("runner %s joined, but installing its service failed: %w; fix that and run runner start again", cfg.NodeID, err)
	}
	if err = c.opts.runnerSvc.Start(ctx); err != nil {
		return fmt.Errorf("runner %s joined, but starting its service failed: %w", cfg.NodeID, err)
	}
	_ = os.Remove(filepath.Join(dir, stoppedFile))
	c.printf("The runner is running, and starts again after a crash or a reboot until you run: runner stop\n")
	return nil
}

func (c *cmds) runnerStop(ctx cli.CommandContext) error {
	if c.opts.euid() != 0 {
		return errors.New("runner stop needs root: run it with sudo")
	}
	dir, err := c.runnerDir(ctx)
	if err != nil {
		return err
	}
	if _, err = (State{Dir: dir}).LoadConfig(); err != nil {
		return fmt.Errorf("this machine isn't a runner: %w", err)
	}
	if err := c.opts.runnerSvc.Stop(ctx.Context()); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, stoppedFile), nil, 0o600); err != nil {
		return err
	}
	c.printf("The runner stopped and stays stopped, across reboots, until runner start. Builds it was running fail and are retried on another runner.\n")
	return nil
}

// runnerStatusInfo is what runner status shows.
type runnerStatusInfo struct {
	Joined            bool     `json:"joined"`
	StateDir          string   `json:"state_dir"`
	URL               string   `json:"url,omitempty"`
	NodeID            string   `json:"node_id,omitempty"`
	PoolID            string   `json:"pool_id,omitempty"`
	FirecrackerConfig string   `json:"firecracker_config,omitempty"`
	Relay             []string `json:"relay,omitempty"`
	KVM               string   `json:"kvm"`
	Running           bool     `json:"running"`
	StartsAtBoot      bool     `json:"starts_at_boot"`
	StoppedByOwner    bool     `json:"stopped_by_owner"`
}

func (c *cmds) runnerStatus(ctx cli.CommandContext) error {
	dir, err := c.runnerDir(ctx)
	if err != nil {
		return err
	}
	st := State{Dir: dir}
	info := runnerStatusInfo{StateDir: dir, KVM: "ready"}
	if kerr := c.opts.kvm(); kerr != nil {
		info.KVM = kerr.Error()
	}
	if cfg, lerr := st.LoadConfig(); lerr == nil && cfg.Role == RoleRunner {
		info.Joined, info.URL, info.NodeID, info.PoolID, info.FirecrackerConfig = true, cfg.URL, cfg.NodeID, cfg.PoolID, cfg.FirecrackerConfig
		if ceiling, cerr := st.ReadCeiling(func(uid uint32) bool { return uid == 0 || int64(uid) == int64(c.opts.euid()) }); cerr == nil {
			info.Relay = ceiling.Relay
		}
	}
	if c.opts.goos == "linux" {
		info.Running, info.StartsAtBoot = c.opts.runnerSvc.State(ctx.Context())
	}
	_, serr := os.Stat(filepath.Join(dir, stoppedFile))
	info.StoppedByOwner = serr == nil
	if ctx.Bool("json") {
		enc := json.NewEncoder(c.opts.Out)
		enc.SetIndent("", "  ")
		return enc.Encode(info)
	}
	if !info.Joined {
		c.printf("This machine isn't a runner. Join one with: sudo loomplane runner start --url ... --token ...\nKVM: %s\n", info.KVM)
		return nil
	}
	state := "stopped"
	switch {
	case info.Running:
		state = "running"
	case info.StoppedByOwner:
		state = "stopped by its owner"
	case info.StartsAtBoot:
		state = "not running (it restarts after a crash; check journalctl -u loomplane-runner)"
	}
	c.printf("Runner %s in pool %s of %s\n  %s, starts at boot: %v\n  KVM: %s\n  Firecracker config: %s\n  relays to: %s\n",
		info.NodeID, info.PoolID, info.URL, state, info.StartsAtBoot, info.KVM, info.FirecrackerConfig, strings.Join(nonEmpty(info.Relay, "nothing"), ", "))
	return nil
}

func nonEmpty(s []string, empty string) []string {
	if len(s) == 0 {
		return []string{empty}
	}
	return s
}

func (c *cmds) runnerLeave(ctx cli.CommandContext) error {
	if c.opts.euid() != 0 && c.opts.goos == "linux" {
		return errors.New("runner leave needs root: run it with sudo")
	}
	dir, err := c.runnerDir(ctx)
	if err != nil {
		return err
	}
	st := State{Dir: dir}
	cfg, err := st.LoadConfig()
	if errors.Is(err, fs.ErrNotExist) {
		c.printf("This machine isn't a runner.\n")
		return c.opts.runnerSvc.Remove(ctx.Context())
	}
	if err != nil {
		return err
	}
	note, cerr := controlCallNote(ctx.Context(), filepath.Join(dir, controlSocket), controlRequest{Op: "leave"})
	switch {
	case cerr == nil && note != "":
		c.printf("%s.\n", note)
	case cerr == nil:
		c.printf("Loomplane revoked this runner.\n")
	case errors.Is(cerr, errNoService):
		c.printf("The runner isn't running, so it can't tell Loomplane it is leaving: revoke runner %s in the dashboard (Runners), or with loomctl node revoke.\n", cfg.NodeID)
	default:
		return fmt.Errorf("the running runner couldn't confirm Loomplane revoked it (%w); revoke it in the dashboard, then run runner leave again", cerr)
	}
	if err = c.opts.runnerSvc.Remove(ctx.Context()); err != nil {
		return fmt.Errorf("removing the service: %w", err)
	}
	if err := st.DeleteConfig(); err != nil {
		return err
	}
	_ = os.Remove(filepath.Join(dir, stoppedFile))
	c.printf("Removed this runner's service and config.\n")
	return nil
}

func (c *cmds) runnerRun(ctx cli.CommandContext) error {
	dir, err := c.runnerDir(ctx)
	if err != nil {
		return err
	}
	return c.opts.runRunner(ctx.Context(), State{Dir: dir})
}

// runRunnerForeground keeps the runner's connection to Loomplane until it
// is told to stop, revoked or refused as too old.
func (c *cmds) runRunnerForeground(ctx context.Context, st State) error {
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	rt, err := NewRuntimeWith(st, &Registry{}, RuntimeOptions{RootAllowlist: true})
	if err != nil {
		return err
	}
	rt.Log = slog.New(slog.NewTextHandler(os.Stderr, nil))
	rt.detectBase = c.opts.detect
	host, err := rt.registerRunner(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = host.Close() }()
	return rt.Run(ctx)
}

func (c *cmds) runnerRelayAllow(ctx cli.CommandContext) error {
	if c.opts.euid() != 0 && c.opts.goos == "linux" {
		return errors.New("runner relay allow needs root: run it with sudo")
	}
	dir, err := c.runnerDir(ctx)
	if err != nil {
		return err
	}
	add := ctx.Args()
	if len(add) == 0 || len(ParseRelayAllow(add)) != len(add) {
		return errors.New("give the networks as CIDR prefixes, such as: runner relay allow 10.0.0.0/16")
	}
	st := State{Dir: dir}
	cfg, err := st.LoadConfig()
	if err != nil {
		return fmt.Errorf("this machine isn't a runner: %w", err)
	}
	ceiling, err := st.ReadCeiling(func(uid uint32) bool { return uid == 0 || int64(uid) == int64(c.opts.euid()) })
	if err != nil || ceiling.NodeID != cfg.NodeID {
		ceiling = Ceiling{NodeID: cfg.NodeID, Endpoints: slices.Clone(runnerEndpoints)}
	}
	for _, p := range ParseRelayAllow(add) {
		if !slices.Contains(ceiling.Relay, p.String()) {
			ceiling.Relay = append(ceiling.Relay, p.String())
		}
	}
	if err := st.SaveCeiling(ceiling, -1); err != nil {
		return err
	}
	c.printf("This runner relays deploys to: %s. The next relayed connection uses it.\n", strings.Join(ceiling.Relay, ", "))
	return nil
}

// nodeStart joins this machine as a node if it hasn't, else starts its
// service again and enables it, so it survives reboots. --once runs it in
// this terminal instead.
func (c *cmds) nodeStart(ctx cli.CommandContext) error {
	dir, err := c.stateDir(ctx)
	if err != nil {
		return err
	}
	st := c.stateFor(dir)
	cfg, lerr := st.LoadConfig()
	if errors.Is(lerr, fs.ErrNotExist) {
		return c.join(ctx)
	}
	if lerr != nil {
		return lerr
	}
	if runner, rerr := (State{Dir: DefaultRunnerStateDir}).LoadConfig(); c.opts.goos == "linux" && rerr == nil && runner.Role == RoleRunner {
		return fmt.Errorf("this machine is runner %s; a machine is a node or a runner, never both", runner.NodeID)
	}
	_ = os.Remove(filepath.Join(dir, stoppedFile))
	if ctx.Bool("once") || ctx.Bool("foreground") {
		c.printf("Running node %s in this terminal. Press Ctrl+C to stop.\n", cfg.NodeID)
		return c.opts.runNode(ctx.Context(), State{Dir: dir}, RuntimeOptions{})
	}
	if err := c.needRoot("node start"); err != nil {
		return err
	}
	if err := c.opts.svc.Start(ctx.Context()); err != nil {
		return err
	}
	c.printf("Node %s is running, and starts again after a crash or a reboot until you run: node stop\n", cfg.NodeID)
	return nil
}

// nodeStop stops the node's service and keeps it stopped across reboots,
// until node start.
func (c *cmds) nodeStop(ctx cli.CommandContext) error {
	if err := c.needRoot("node stop"); err != nil {
		return err
	}
	dir, err := c.stateDir(ctx)
	if err != nil {
		return err
	}
	if _, err = c.stateFor(dir).LoadConfig(); err != nil {
		return fmt.Errorf("this machine isn't a node: %w", err)
	}
	if d, ok := c.opts.svc.(interface{ Disable(context.Context) error }); ok {
		err = d.Disable(ctx.Context())
	} else {
		err = c.opts.svc.Stop(ctx.Context())
	}
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, stoppedFile), nil, 0o600); err != nil {
		return err
	}
	_ = c.handBack(dir, stoppedFile) //nolint:errcheck // the mark is only for status
	c.printf("The node stopped and stays stopped, across reboots, until node start. What it runs keeps running.\n")
	return nil
}
