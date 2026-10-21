package ctl

import (
	"errors"
	"strconv"

	"github.com/xraph/forge/cli"

	"github.com/xraph/loomplane/internal/recovery"
)

func (a *App) previewBackupRetention(ctx cli.CommandContext) error {
	if ctx.Arg(0) == "" {
		return errors.New("give a backup ID from the database whose history you want to preview")
	}
	input, err := retentionSettings(ctx)
	if err != nil {
		return err
	}
	c, err := a.client()
	if err != nil {
		return err
	}
	preview, err := c.BackupRetentionPreview(ctxOf(ctx), ctx.Arg(0), input.OlderThanDays, input.KeepLast)
	if err != nil {
		return err
	}
	if ctx.Bool("json") {
		return a.json(preview)
	}
	a.printf("Read-only preview. No backups were deleted.\n")
	if !preview.Complete {
		a.printf("The inventory is incomplete. No recovery points were selected.\n")
	}
	a.printf("%d recovery points (%d bytes) meet the age and count settings.\n", preview.EligibleCount, preview.EligibleBytes)
	rows := make([][]string, 0, len(preview.Points))
	for _, point := range preview.Points {
		rows = append(rows, []string{point.BackupID, strconv.FormatInt(point.Bytes, 10), point.Reason})
	}
	a.table([]string{"BACKUP", "BYTES", "RETENTION DECISION"}, rows)
	return nil
}

func retentionSettingFlags() []cli.Flag {
	return []cli.Flag{cli.NewStringFlag("older-than-days", "", "Only consider backups completed before this age in days", "30"), cli.NewStringFlag("keep-last", "", "Minimum number of newest successful points to retain", "7")}
}

func retentionSettings(ctx cli.CommandContext) (recovery.RetentionInput, error) {
	days, err := strconv.Atoi(ctx.String("older-than-days"))
	if err != nil {
		return recovery.RetentionInput{}, errors.New("older-than-days must be an integer")
	}
	keep, err := strconv.Atoi(ctx.String("keep-last"))
	if err != nil {
		return recovery.RetentionInput{}, errors.New("keep-last must be an integer")
	}
	input := recovery.RetentionInput{OlderThanDays: days, KeepLast: keep}
	return input, input.Validate()
}
