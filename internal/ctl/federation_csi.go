package ctl

import (
	"errors"

	"github.com/xraph/forge/cli"

	"github.com/xraph/loomplane/internal/agentinstall"
)

func (a *App) federationCommand() cli.Command {
	defaults := agentinstall.DefaultCSIConfig("")
	return cli.NewCommand("federation", "Prepare cluster federation components", func(cli.CommandContext) error { return errors.New("choose federation csi-manifest") }, cli.WithSubcommand(cli.NewCommand("csi-manifest", "Render SPIFFE CSI resources for cluster-admin review", a.csiManifest, cli.WithFlags(
		cli.NewStringFlag("namespace", "", "Existing SPIRE namespace", ""),
		cli.NewStringFlag("driver-image", "", "Digest-pinned SPIFFE CSI image", defaults.DriverImage),
		cli.NewStringFlag("registrar-image", "", "Digest-pinned CSI registrar image", defaults.RegistrarImage),
		cli.NewStringFlag("agent-socket-directory", "", "Host directory containing the SPIRE Workload API socket", defaults.AgentSocketDirectory),
		cli.NewStringFlag("kubelet-root-directory", "", "Kubelet root on the Linux nodes", defaults.KubeletRootDirectory),
	))))
}
func (a *App) csiManifest(ctx cli.CommandContext) error {
	if len(ctx.Args()) != 0 {
		return errors.New("csi-manifest accepts flags only")
	}
	manifest, err := agentinstall.CSIManifest(agentinstall.CSIConfig{Namespace: ctx.String("namespace"), DriverImage: ctx.String("driver-image"), RegistrarImage: ctx.String("registrar-image"), AgentSocketDirectory: ctx.String("agent-socket-directory"), KubeletRootDirectory: ctx.String("kubelet-root-directory")})
	if err != nil {
		return err
	}
	return a.json(manifest)
}
