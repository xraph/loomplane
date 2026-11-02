package ctl

import (
	"errors"
	"fmt"
	"strconv"

	"github.com/xraph/forge/cli"

	"github.com/xraph/loomplane/pkg/client"
)

func (a *App) nativeRetentionPolicyCommands() []cli.Command {
	flags := append(retentionPolicyWriteFlags(), retentionSettingFlags()...)
	flags = append(flags, cli.NewStringFlag("revision", "", "Observed policy revision, or 0 to create", "0"))
	return []cli.Command{
		cli.NewCommand("policy", "Inspect the native retention policy for a captured source", a.showNativeRetentionPolicy, cli.WithFlag(jsonFlag)),
		cli.NewCommand("policy-set", "Enable recurring native deletion with a reviewed age and count rule", a.setNativeRetentionPolicy, cli.WithFlags(flags...)),
		cli.NewCommand("policy-pause", "Stop future native selections; accepted jobs may finish", a.pauseNativeRetentionPolicy, cli.WithFlags(retentionPolicyWriteFlags()...)),
		cli.NewCommand("policy-history", "Read committed native retention policy revisions", a.nativeRetentionPolicyHistory, cli.WithFlags(retirementPageFlags()...)),
	}
}
func nativePolicyWriteCheck(ctx cli.CommandContext) error {
	if !ctx.Bool("yes") {
		return errors.New("pass --yes to change automatic native backup deletion")
	}
	if ctx.Arg(0) == "" {
		return errors.New("give a retained native source ID")
	}
	return nil
}
func (a *App) showNativeRetentionPolicy(ctx cli.CommandContext) error {
	if ctx.Arg(0) == "" {
		return errors.New("give a retained native source ID")
	}
	c, err := a.client()
	if err != nil {
		return err
	}
	state, err := c.NativeRetentionPolicy(ctxOf(ctx), ctx.Arg(0))
	if err != nil {
		return err
	}
	return a.json(state)
}
func (a *App) setNativeRetentionPolicy(ctx cli.CommandContext) error {
	if err := nativePolicyWriteCheck(ctx); err != nil {
		return err
	}
	revision, err := strconv.ParseInt(ctx.String("revision"), 10, 64)
	if err != nil || revision < 0 || revision >= 2147483647 {
		return errors.New("revision must be the observed policy revision, or 0 to create")
	}
	settings, err := retentionSettings(ctx)
	if err != nil {
		return err
	}
	c, err := a.client()
	if err != nil {
		return err
	}
	saved, err := c.SetNativeRetentionPolicy(ctxOf(ctx), ctx.Arg(0), client.RetentionPolicyInput{Revision: revision, Enabled: true, Policy: client.RetentionSettings{OlderThanDays: settings.OlderThanDays, KeepLast: settings.KeepLast}})
	if err != nil {
		return fmt.Errorf("native policy save failed; inspect backups native-retention policy %s before retrying: %w", ctx.Arg(0), err)
	}
	return a.json(saved)
}
func (a *App) pauseNativeRetentionPolicy(ctx cli.CommandContext) error {
	if err := nativePolicyWriteCheck(ctx); err != nil {
		return err
	}
	c, err := a.client()
	if err != nil {
		return err
	}
	state, err := c.NativeRetentionPolicy(ctxOf(ctx), ctx.Arg(0))
	if err != nil {
		return err
	}
	if !state.Configured {
		return a.json(state)
	}
	if state.Policy == nil || state.Policy.Revision < 1 {
		return errors.New("native policy response is incomplete; inspect it before retrying")
	}
	saved, err := c.SetNativeRetentionPolicy(ctxOf(ctx), ctx.Arg(0), client.RetentionPolicyInput{Revision: state.Policy.Revision, Enabled: false, Policy: state.Policy.Policy})
	if err != nil {
		return fmt.Errorf("native policy pause failed; inspect backups native-retention policy %s before retrying: %w", ctx.Arg(0), err)
	}
	return a.json(saved)
}
func (a *App) nativeRetentionPolicyHistory(ctx cli.CommandContext) error {
	if ctx.Arg(0) == "" {
		return errors.New("give a retained native source ID")
	}
	in, err := retirementPageInput(ctx)
	if err != nil {
		return err
	}
	if checkErr := validateHistoryBeforeCursor(in.Before); checkErr != nil {
		return checkErr
	}
	c, err := a.client()
	if err != nil {
		return err
	}
	page, err := c.NativeRetentionPolicyHistory(ctxOf(ctx), ctx.Arg(0), in)
	if err != nil {
		return err
	}
	return a.json(page)
}
