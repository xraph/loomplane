// Package ctl is loomctl, Loomplane's command-line client. It calls the API
// with an API key through pkg/client, so anything it does, a script can do
// the same way.
//
// It finds the server and key in LOOMPLANE_URL and LOOMPLANE_API_KEY, which
// suit CI, or in the file `loomctl login` writes, readable by you only.
package ctl

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/xraph/forge/cli"

	"github.com/xraph/loomplane/internal/recipes"
	"github.com/xraph/loomplane/pkg/client"
)

// Environment variables loomctl reads. They win over the saved login.
const (
	EnvURL = "LOOMPLANE_URL"
	EnvKey = "LOOMPLANE_API_KEY"
)

// Settings are where loomctl finds Loomplane.
type Settings struct {
	URL string `json:"url"`
	Key string `json:"key"`
}

// Options configure a loomctl. Tests replace all of them.
type Options struct {
	Version string
	Out     io.Writer
	In      io.Reader
	// Getenv reads the environment.
	Getenv func(string) string
	// ConfigPath is the saved login. Empty means the user's config
	// directory.
	ConfigPath string
	// Recipes describes the catalog kinds. Nil means the registry the
	// server binary registers its kinds in.
	Recipes *recipes.Registry
}

// App is loomctl.
type App struct {
	opts Options
}

// New returns loomctl.
func New(opts Options) *App {
	if opts.Out == nil {
		opts.Out = os.Stdout
	}
	if opts.In == nil {
		opts.In = os.Stdin
	}
	if opts.Getenv == nil {
		opts.Getenv = os.Getenv
	}
	if opts.Recipes == nil {
		opts.Recipes = recipes.Default()
	}
	if opts.ConfigPath == "" {
		dir, err := os.UserConfigDir()
		if err != nil {
			dir = "."
		}
		opts.ConfigPath = filepath.Join(dir, "loomplane", "loomctl.json")
	}
	return &App{opts: opts}
}

// Run runs the command line args, without the program name.
func (a *App) Run(args []string) error {
	c := cli.New(cli.Config{
		Name: "loomctl", Version: a.opts.Version, Output: a.opts.Out,
		Description: "Plan, approve and follow Loomplane deployments from your terminal or CI",
	})
	for _, cmd := range a.commands() {
		if err := c.AddCommand(cmd); err != nil {
			return err
		}
	}
	// Forge's CLI reads arguments the way os.Args has them, after the
	// program's name.
	return c.Run(append([]string{"loomctl"}, args...))
}

// settings reads where to find Loomplane: the environment, then the saved
// login.
func (a *App) settings() (Settings, error) {
	var s Settings
	if data, err := os.ReadFile(a.opts.ConfigPath); err == nil {
		if jerr := json.Unmarshal(data, &s); jerr != nil {
			return s, fmt.Errorf("read %s: %w", a.opts.ConfigPath, jerr)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return s, err
	}
	if v := a.opts.Getenv(EnvURL); v != "" {
		s.URL = v
	}
	if v := a.opts.Getenv(EnvKey); v != "" {
		s.Key = v
	}
	return s, nil
}

func (a *App) save(s Settings) error {
	if err := os.MkdirAll(filepath.Dir(a.opts.ConfigPath), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	// The key is a credential: readable by you only.
	return os.WriteFile(a.opts.ConfigPath, append(data, '\n'), 0o600)
}

// client returns a client for the saved or given settings.
func (a *App) client() (*client.Client, error) {
	s, err := a.settings()
	if err != nil {
		return nil, err
	}
	if s.URL == "" || s.Key == "" {
		return nil, fmt.Errorf("loomctl doesn't know where Loomplane is: run `loomctl login`, or set %s and %s", EnvURL, EnvKey)
	}
	return client.New(s.URL, s.Key), nil
}

// project resolves a project argument: its ID, slug or name.
func (a *App) project(ctx cli.CommandContext, c *client.Client) (*client.Project, error) {
	ref := ctx.Arg(0)
	if ref == "" {
		return nil, errors.New("name the project: its slug, name or ID")
	}
	return c.FindProject(ctx.Context(), ref)
}

func (a *App) json(v any) error {
	enc := json.NewEncoder(a.opts.Out)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func (a *App) table(header []string, rows [][]string) {
	tw := tabwriter.NewWriter(a.opts.Out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, strings.Join(header, "\t"))
	for _, r := range rows {
		fmt.Fprintln(tw, strings.Join(r, "\t"))
	}
	_ = tw.Flush()
}

func (a *App) printf(format string, args ...any) {
	fmt.Fprintf(a.opts.Out, format, args...)
}

// short shortens a commit. A deployment of several repositories names each
// at its short commit already, as "web@3f2c1a9 api@9a1b02e", and stays
// whole.
func short(s string) string {
	if len(s) > 12 && !strings.Contains(s, "@") {
		return s[:12]
	}
	return s
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// ctxOf is the command's context.
func ctxOf(ctx cli.CommandContext) context.Context { return ctx.Context() }

func readAll(r io.Reader) ([]byte, error) {
	return io.ReadAll(io.LimitReader(r, 1<<20))
}

func afterEvery() <-chan time.Time { return time.After(client.Every) }
