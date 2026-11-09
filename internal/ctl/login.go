package ctl

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/xraph/forge/cli"

	"github.com/xraph/loomplane/pkg/client"
)

// loginCommand signs loomctl in. With no key it signs you in as yourself:
// it prints a code, opens the dashboard's /device page, and waits while you
// approve it there. --key saves an API key instead, which CI wants.
func (a *App) loginCommand() cli.Command {
	return cli.NewCommand("login", "Sign in as yourself in the browser, or save an API key", a.login,
		cli.WithUsage("login --url https://loomplane.example.com [--org acme] | login --url ... --key lp_live_..."),
		cli.WithFlags(
			cli.NewStringFlag("url", "", "Loomplane's address", ""),
			cli.NewStringFlag("key", "", "An API key from Settings, API keys, instead of signing in; - reads it from standard input", ""),
			cli.NewStringFlag("org", "", "The organization to act in, by slug, name or ID; asked when you belong to several", ""),
			cli.NewBoolFlag("no-browser", "", "Print the address to open instead of opening it", false),
		))
}

func (a *App) login(ctx cli.CommandContext) error {
	s, err := a.settings()
	if err != nil {
		return err
	}
	if v := ctx.String("url"); v != "" {
		s.URL = strings.TrimRight(v, "/")
	}
	if s.URL == "" {
		return errors.New("give --url, Loomplane's address")
	}
	key := ctx.String("key")
	if key == "" {
		return a.deviceLogin(ctx, s.URL)
	}
	if key == "-" {
		line, rerr := bufio.NewReader(a.opts.In).ReadString('\n')
		if rerr != nil && line == "" {
			return fmt.Errorf("read the key: %w", rerr)
		}
		key = line
	}
	if key = strings.TrimSpace(key); key == "" {
		return errors.New("the key is empty; make one under Settings, API keys")
	}
	who, err := client.New(s.URL, key).Session(ctxOf(ctx))
	if err != nil {
		return fmt.Errorf("that key doesn't work at %s: %w", s.URL, err)
	}
	// A key replaces any sign-in, refresh token and all.
	if err := a.save(Settings{URL: s.URL, Key: key}); err != nil {
		return err
	}
	a.printf("Logged in to %s, in organization %s.\n", s.URL, who.OrgID)
	return nil
}

// deviceLogin signs you in through the browser (RFC 8628).
func (a *App) deviceLogin(ctx cli.CommandContext, url string) error {
	c := ctxOf(ctx)
	login, err := client.StartDeviceLogin(c, url)
	if err != nil {
		return err
	}
	a.printf("To sign in, open %s\nand enter the code %s\n", login.VerificationURI, login.UserCode)
	if !ctx.Bool("no-browser") {
		if a.opts.OpenBrowser(login.VerificationURIComplete) == nil {
			a.printf("Opened it in your browser.\n")
		}
	}
	a.printf("Waiting for you to approve it...\n")
	tok, err := login.Wait(c)
	if err != nil {
		return err
	}
	lp := client.New(url, tok.AccessToken)
	org, err := a.chooseOrganization(c, lp, ctx.String("org"))
	if err != nil {
		// The token works, so don't leave it alive and unsaved.
		_ = client.RevokeToken(c, url, tok.AccessToken) //nolint:errcheck // best effort; the error that matters is err
		return err
	}
	if _, err := lp.UseOrganization(c, org.ID); err != nil {
		return fmt.Errorf("act in %s: %w", org.Name, err)
	}
	if err := a.save(Settings{URL: url, Key: tok.AccessToken, RefreshToken: tok.RefreshToken, ExpiresAt: tok.ExpiresAt}); err != nil {
		return err
	}
	a.printf("Logged in to %s, in %s (%s).\n", url, org.Name, org.Slug)
	return nil
}

// chooseOrganization picks the organization a sign-in acts in: the one
// named, the only one, or the one you choose from a list.
func (a *App) chooseOrganization(ctx context.Context, c *client.Client, want string) (client.Organization, error) {
	list, err := c.Organizations(ctx)
	if err != nil {
		return client.Organization{}, fmt.Errorf("list your organizations: %w", err)
	}
	orgs := list.Organizations
	if want != "" {
		for _, o := range orgs {
			if o.ID == want || strings.EqualFold(o.Slug, want) || strings.EqualFold(o.Name, want) {
				return o, nil
			}
		}
		return client.Organization{}, fmt.Errorf("you aren't in an organization called %q; you're in %s", want, slugs(orgs))
	}
	switch len(orgs) {
	case 0:
		return client.Organization{}, errors.New("you aren't in any organization yet: create one in the dashboard, then log in again")
	case 1:
		return orgs[0], nil
	}
	a.printf("You're in %d organizations:\n", len(orgs))
	for i, o := range orgs {
		a.printf("  %d. %s (%s)\n", i+1, o.Name, o.Slug)
	}
	a.printf("Which one should loomctl act in? ")
	line, rerr := bufio.NewReader(a.opts.In).ReadString('\n')
	if n, err := strconv.Atoi(strings.TrimSpace(line)); err == nil && n >= 1 && n <= len(orgs) {
		return orgs[n-1], nil
	}
	answer := strings.TrimSpace(line)
	for _, o := range orgs {
		if answer != "" && (strings.EqualFold(o.Slug, answer) || strings.EqualFold(o.Name, answer)) {
			return o, nil
		}
	}
	if rerr != nil && answer == "" {
		return client.Organization{}, fmt.Errorf("pick one with --org: %s", slugs(orgs))
	}
	return client.Organization{}, fmt.Errorf("%q isn't one of them; pick one with --org: %s", answer, slugs(orgs))
}

func slugs(orgs []client.Organization) string {
	out := make([]string, 0, len(orgs))
	for _, o := range orgs {
		out = append(out, o.Slug)
	}
	return strings.Join(out, ", ")
}

func (a *App) logout(ctx cli.CommandContext) error {
	s, err := a.saved()
	if err != nil {
		return err
	}
	revoked := false
	if s.RefreshToken != "" && s.URL != "" {
		// Ending the session server-side is the point of logging out of a
		// sign-in; a failure still forgets it here.
		revoked = client.RevokeToken(ctxOf(ctx), s.URL, s.Key) == nil
	}
	if err := os.Remove(a.opts.ConfigPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	switch {
	case revoked:
		a.printf("Logged out, and ended the session.\n")
	case s.RefreshToken != "":
		a.printf("Logged out. Loomplane couldn't be reached to end the session; it ends when it expires.\n")
	default:
		a.printf("Logged out. The key still works until you revoke it under Settings, API keys.\n")
	}
	return nil
}

// renewBefore is how close to expiry a sign-in's token is renewed.
const renewBefore = time.Minute

// renew refreshes a sign-in's access token when it's about to run out, and
// saves the new one. API keys, and anything the environment sets, are left
// alone.
func (a *App) renew(s *Settings) error {
	if s.RefreshToken == "" || a.opts.Getenv(EnvURL) != "" || a.opts.Getenv(EnvKey) != "" {
		return nil
	}
	if time.Until(s.ExpiresAt) > renewBefore {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	tok, err := client.RefreshToken(ctx, s.URL, s.RefreshToken)
	if err != nil {
		return fmt.Errorf("your sign-in to %s has ended: run `loomctl login` again (%w)", s.URL, err)
	}
	s.Key, s.RefreshToken, s.ExpiresAt = tok.AccessToken, tok.RefreshToken, tok.ExpiresAt
	return a.save(*s)
}

// openBrowser opens url in the default browser.
func openBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.CommandContext(context.Background(), "open", url) // #nosec G204 -- fixed opener, URL as its only argument, no shell.
	case "windows":
		cmd = exec.CommandContext(context.Background(), "rundll32", "url.dll,FileProtocolHandler", url) // #nosec G204 -- fixed opener, URL as its only argument, no shell.
	default:
		cmd = exec.CommandContext(context.Background(), "xdg-open", url) // #nosec G204 -- fixed opener, URL as its only argument, no shell.
	}
	return cmd.Start()
}

// organizationsCommand lists and switches the organizations a sign-in acts
// in. An API key belongs to one organization and can do neither.
func (a *App) organizationsCommand() cli.Command {
	return cli.NewCommand("organizations", "List the organizations your sign-in can act in", a.organizations,
		cli.WithFlag(jsonFlag),
		cli.WithSubcommand(cli.NewCommand("use", "Act in another of your organizations", a.useOrganization,
			cli.WithUsage("organizations use <slug|name|id>"))))
}

func (a *App) organizations(ctx cli.CommandContext) error {
	c, err := a.client()
	if err != nil {
		return err
	}
	list, err := c.Organizations(ctxOf(ctx))
	if err != nil {
		return signInOnly(err)
	}
	if ctx.Bool("json") {
		return a.json(list)
	}
	rows := make([][]string, 0, len(list.Organizations))
	for _, o := range list.Organizations {
		mark := ""
		if o.ID == list.Active {
			mark = "*"
		}
		rows = append(rows, []string{mark, o.Slug, o.Name, o.ID})
	}
	a.table([]string{"", "SLUG", "NAME", "ID"}, rows)
	return nil
}

func (a *App) useOrganization(ctx cli.CommandContext) error {
	if ctx.Arg(0) == "" {
		return errors.New("name the organization: its slug, name or ID")
	}
	c, err := a.client()
	if err != nil {
		return err
	}
	org, err := a.chooseOrganization(ctxOf(ctx), c, ctx.Arg(0))
	if err != nil {
		return signInOnly(err)
	}
	if _, err := c.UseOrganization(ctxOf(ctx), org.ID); err != nil {
		return err
	}
	a.printf("loomctl now acts in %s (%s).\n", org.Name, org.Slug)
	return nil
}

// signInOnly explains a 401 from the organization routes, which only a
// sign-in may call.
func signInOnly(err error) error {
	if errors.Is(err, client.ErrUnauthorized) {
		return errors.New("an API key acts in its one organization; run `loomctl login` without --key to sign in as yourself and switch")
	}
	return err
}
