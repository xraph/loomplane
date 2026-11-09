// Command loomplane-qualify is the model qualification job. The
// qualify-models workflow runs it once a week; nothing else should.
//
//	loomplane-qualify plan  --config .github/qualification.yaml [--week 2026-W45]
//	loomplane-qualify run   --config .github/qualification.yaml --confirm --out results.json [--run-id gh-<run>-<attempt>]
//	loomplane-qualify sweep --config .github/qualification.yaml [--run-id gh-<run>-<attempt>] [--only-expired=false]
//	loomplane-qualify merge [--data internal/models/registry/data/qualification.yaml] [--summary summary.md] results.json
//
// plan prints what a run would sample this week and its worst-case cost,
// and spends nothing. run creates one GPU Droplet in the qualification
// account, qualifies the samples on it and deletes it; it costs money, so
// it needs --confirm. sweep deletes the Droplets the job tagged once their
// run has expired, and their SSH keys, so nothing a crashed run left keeps
// billing and nothing a live run uses goes. With --run-id it first deletes
// that run's Droplet and key whatever their expiry, which is how the
// workflow cleans up after a cancelled run. merge folds a run's results
// into the registry's qualification.yaml.
//
// run and sweep read the DigitalOcean token from QUALIFY_DO_TOKEN and the
// account's UUID from the config or QUALIFY_DO_ACCOUNT_UUID, and refuse a
// token from any other account. run reads a Hugging Face token for gated
// weights from QUALIFY_HF_TOKEN, and the speech probe's recording from
// internal/models/qualify/testdata/speech.wav when it's there. Tokens never reach a log line, an error,
// the results or a container's arguments.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing/fstest"
	"time"

	"github.com/xraph/loomplane/internal/clouds/digitalocean"
	"github.com/xraph/loomplane/internal/clouds/gpusizes"
	"github.com/xraph/loomplane/internal/models/qualify"
	"github.com/xraph/loomplane/internal/models/registry"
)

// The environment the command reads secrets from. They're the
// model-qualification environment's secrets in the workflow.
const (
	doTokenEnv = "QUALIFY_DO_TOKEN"
	hfTokenEnv = "QUALIFY_HF_TOKEN" //nolint:gosec // G101: names a variable, holds no credential
)

const defaultData = "internal/models/registry/data/qualification.yaml"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Getenv, os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

const usage = `usage: loomplane-qualify plan|run|sweep|merge [flags]
  plan   print this week's samples and worst-case cost; spends nothing
  run    qualify this week's samples on a GPU Droplet; costs money, needs --confirm
  sweep  delete the job's expired Droplets and their SSH keys
  merge  fold a run's results.json into qualification.yaml`

// run is the command; it returns the exit code.
func run(ctx context.Context, args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, usage)
		return 2
	}
	var err error
	switch args[0] {
	case "plan":
		err = planCommand(args[1:], getenv, stdout)
	case "run":
		err = runCommand(ctx, args[1:], getenv, stdout, stderr)
	case "sweep":
		err = sweepCommand(ctx, args[1:], getenv, stdout)
	case "merge":
		err = mergeCommand(args[1:], stdout)
	default:
		fmt.Fprintln(stderr, usage)
		return 2
	}
	if err != nil {
		fmt.Fprintln(stderr, "loomplane-qualify:", err)
		return 1
	}
	return 0
}

// loadConfig reads the config and fills the account UUID from the
// environment when the file leaves it empty.
func loadConfig(path string, getenv func(string) string) (qualify.Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return qualify.Config{}, err
	}
	defer f.Close()
	cfg, err := qualify.LoadConfig(f)
	if err != nil {
		return cfg, err
	}
	return cfg.WithAccount(getenv(qualify.AccountEnv))
}

func planCommand(args []string, getenv func(string) string, stdout io.Writer) error {
	fl := flag.NewFlagSet("plan", flag.ContinueOnError)
	config := fl.String("config", ".github/qualification.yaml", "the qualification config")
	week := fl.String("week", "", "the ISO week to plan, like 2026-W45; empty is this week")
	if err := fl.Parse(args); err != nil {
		return err
	}
	cfg, err := loadConfig(*config, getenv)
	if err != nil {
		return err
	}
	if *week == "" {
		*week = qualify.CurrentWeek(time.Now())
	}
	r := registry.Default()
	kind, samples := qualify.Pick(r, r.Results(), cfg, *week)
	if kind == "" {
		return fmt.Errorf("%q isn't an ISO week like 2026-W45, or the config lists no GPU kinds", *week)
	}
	fmt.Fprintf(stdout, "Week %s runs on %s, %d samples, about %.1f hours:\n", *week, kind, len(samples), qualify.PlannedHours(r, samples))
	for _, s := range samples {
		fmt.Fprintf(stdout, "  %s\n", s)
	}
	price := "unknown"
	billed := cfg.MaxRunHours + qualify.TeardownHours
	for _, size := range gpusizes.For("digitalocean") {
		if a := size.Accelerator; a != nil && a.Kind == kind && a.Count == qualify.DropletGPUs && size.HourlyPriceKnown {
			price = fmt.Sprintf("$%.2f (%s at Loomplane's reference price of $%.2f an hour, as of %s, for %g hours with teardown)",
				size.HourlyUSD*billed, size.ID, size.HourlyUSD, gpusizes.PricesAsOf, billed)
		}
	}
	fmt.Fprintf(stdout, "Worst case: %s. Caps: $%s a month, $%s a run.\n", price, cfg.MonthlyCapUSD, cfg.RunCapUSD)
	return nil
}

func runCommand(ctx context.Context, args []string, getenv func(string) string, stdout, stderr io.Writer) error {
	fl := flag.NewFlagSet("run", flag.ContinueOnError)
	config := fl.String("config", ".github/qualification.yaml", "the qualification config")
	confirm := fl.Bool("confirm", false, "create a GPU Droplet, which costs money")
	out := fl.String("out", "results.json", "where to write the results")
	week := fl.String("week", "", "the ISO week to run, like 2026-W45; empty is this week")
	clip := fl.String("speech", qualify.SpeechClipPath, "the speech probe's recording; speech samples don't run without it")
	runID := fl.String("run-id", "", "the run's ID, gh-<GitHub run id>-<attempt>; empty makes one")
	if err := fl.Parse(args); err != nil {
		return err
	}
	if !*confirm {
		return qualify.ErrNotConfirmed
	}
	if *runID != "" {
		if err := qualify.CheckRunID(*runID); err != nil {
			return err
		}
	}
	cfg, err := loadConfig(*config, getenv)
	if err != nil {
		return err
	}
	token := strings.TrimSpace(getenv(doTokenEnv))
	if token == "" {
		return fmt.Errorf("set %s to the qualification account's DigitalOcean token", doTokenEnv)
	}
	speech, err := readSpeech(*clip)
	if err != nil {
		return err
	}
	client := digitalocean.New("")
	r := registry.Default()
	rep := &qualify.Report{}
	d := qualify.RunDeps{
		Config: cfg, Registry: r, Results: r.Results(), Week: *week, Confirm: *confirm, RunID: *runID,
		RunURL: runURL(getenv), HFToken: strings.TrimSpace(getenv(hfTokenEnv)), Speech: speech,
		Spend:    qualify.NewDigitalOceanSpend("", token),
		Machines: machines(token, cfg),
		Offers:   qualify.NewDigitalOceanOffers(client, token, cfg.AccountUUID),
		Connect:  qualify.Connect,
		Log:      stderr,
		Report:   rep,
	}
	_, runErr := qualify.Run(ctx, d)
	if rep.Kind != "" {
		if wErr := writeReport(*out, rep); wErr != nil {
			return errors.Join(runErr, wErr)
		}
		fmt.Fprintf(stdout, "Wrote %d results and %d samples that didn't run to %s.\n", len(rep.Results), len(rep.NotRun), *out)
	}
	return runErr
}

// readSpeech reads the speech probe's recording, or nil when there's none
// yet. A file that isn't a short 16-bit mono WAV is an error.
func readSpeech(path string) ([]byte, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if cErr := qualify.CheckSpeechClip(b); cErr != nil {
		return nil, fmt.Errorf("%s: %w", path, cErr)
	}
	return b, nil
}

// runURL is the GitHub Actions run, or "local".
func runURL(getenv func(string) string) string {
	server, repo, id := getenv("GITHUB_SERVER_URL"), getenv("GITHUB_REPOSITORY"), getenv("GITHUB_RUN_ID")
	if server == "" || repo == "" || id == "" {
		return "local"
	}
	return strings.TrimRight(server, "/") + "/" + repo + "/actions/runs/" + id
}

func writeReport(path string, rep *qualify.Report) error {
	data, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o600)
}

func sweepCommand(ctx context.Context, args []string, getenv func(string) string, stdout io.Writer) error {
	fl := flag.NewFlagSet("sweep", flag.ContinueOnError)
	config := fl.String("config", ".github/qualification.yaml", "the qualification config")
	onlyExpired := fl.Bool("only-expired", true, "delete only Droplets whose run is past its expiry; false deletes live runs' Droplets too")
	runID := fl.String("run-id", "", "first delete this run's Droplet and SSH key, whatever their expiry")
	if err := fl.Parse(args); err != nil {
		return err
	}
	if *runID != "" {
		if err := qualify.CheckRunID(*runID); err != nil {
			return err
		}
	}
	cfg, err := loadConfig(*config, getenv)
	if err != nil {
		return err
	}
	if vErr := cfg.Validate(); vErr != nil {
		return vErr
	}
	token := strings.TrimSpace(getenv(doTokenEnv))
	if token == "" {
		return fmt.Errorf("set %s to the qualification account's DigitalOcean token", doTokenEnv)
	}
	account, err := qualify.NewDigitalOceanSpend("", token).AccountUUID(ctx)
	if err != nil {
		return fmt.Errorf("couldn't read which account the token belongs to, so nothing is deleted: %w", err)
	}
	if account != cfg.AccountUUID {
		return qualify.ErrWrongAccount
	}
	// Deleting never waits on a cancel: a half-done sweep leaves spend.
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Minute)
	defer cancel()
	m := machines(token, cfg)
	deleted := []string{}
	if *runID != "" {
		gone, dErr := m.DeleteRun(sctx, *runID)
		deleted, err = append(deleted, gone...), dErr
	}
	swept, sErr := m.Sweep(sctx, time.Now(), *onlyExpired)
	deleted, err = append(deleted, swept...), errors.Join(err, sErr)
	for _, s := range deleted {
		fmt.Fprintf(stdout, "Deleted %s.\n", s)
	}
	if len(deleted) == 0 && err == nil {
		fmt.Fprintln(stdout, "Nothing to delete.")
	}
	return err
}

// machines is the DigitalOcean Machines for cfg's runs.
func machines(token string, cfg qualify.Config) *qualify.DigitalOceanMachines {
	m := qualify.NewDigitalOceanMachines("", token)
	m.KeyGrace = cfg.KeyGrace()
	return m
}

func mergeCommand(args []string, stdout io.Writer) error {
	fl := flag.NewFlagSet("merge", flag.ContinueOnError)
	data := fl.String("data", defaultData, "the registry's qualification.yaml")
	summary := fl.String("summary", "", "also write a pull request body here")
	if err := fl.Parse(args); err != nil {
		return err
	}
	if fl.NArg() != 1 {
		return errors.New("merge takes one results.json")
	}
	raw, err := os.ReadFile(fl.Arg(0))
	if err != nil {
		return err
	}
	var rep qualify.Report
	if jErr := json.Unmarshal(raw, &rep); jErr != nil {
		return fmt.Errorf("%s: %w", fl.Arg(0), jErr)
	}
	file, err := os.ReadFile(*data)
	if err != nil {
		return err
	}
	merged, err := qualify.Merge(file, rep.Results)
	if err != nil {
		return err
	}
	if cErr := checkRegistry(*data, merged); cErr != nil {
		return fmt.Errorf("the merged results don't load: %w", cErr)
	}
	if wErr := os.WriteFile(*data, merged, 0o644); wErr != nil { //nolint:gosec // G306: a tracked source file
		return wErr
	}
	fmt.Fprintf(stdout, "Merged %d results into %s.\n", len(rep.Results), *data)
	if *summary != "" {
		return os.WriteFile(*summary, []byte(qualify.Summary(&rep)), 0o600)
	}
	return nil
}

// checkRegistry loads the registry from the data directory with merged in
// place of qualification.yaml, so a merge that would break the build
// never gets written.
func checkRegistry(path string, merged []byte) error {
	dir := filepath.Dir(path)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	files := fstest.MapFS{}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		b, rErr := os.ReadFile(filepath.Join(dir, e.Name()))
		if rErr != nil {
			return rErr
		}
		files[e.Name()] = &fstest.MapFile{Data: b, Mode: fs.ModePerm}
	}
	files[filepath.Base(path)] = &fstest.MapFile{Data: merged, Mode: fs.ModePerm}
	_, err = registry.Load(files)
	return err
}
