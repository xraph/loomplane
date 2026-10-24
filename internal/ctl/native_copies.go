package ctl

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/xraph/forge/cli"

	"github.com/xraph/loomplane/pkg/client"
)

func (a *App) nativeCopyCommand() cli.Command {
	return cli.NewCommand("native-copies", "List temporary provider-native recovery copies", a.nativeCopies,
		cli.WithUsage("backups native-copies <project> [--environment-id ID] [--resource NAME] [--before ID] [--limit 50] [--json]"),
		cli.WithFlags(nativeRecoveryScopeFlags()...),
		cli.WithSubcommand(cli.NewCommand("review", "Review capacity and lifetime before creating a native copy", a.reviewNativeCopy,
			cli.WithFlags(jsonFlag, cli.NewStringFlag("restore-time", "", "Explicit PITR time as a whole UTC second (RFC3339 Z)", ""), cli.NewStringFlag("cloud", "", "Granted destination Cloud ID", ""), cli.NewStringFlag("target", "", "Named destination target ID", ""), cli.NewStringFlag("ttl-hours", "", "Copy lifetime, from 1 to 24 hours", "1"), cli.NewStringFlag("request-id", "", "Stable request ID retained in the review", ""), cli.NewStringFlag("out", "", "Save a private review JSON file without overwriting", "")),
			cli.WithUsage("backups native-copies review <source> <point> --cloud ID --target ID [--restore-time UTC] [--ttl-hours 1] [--request-id KEY] [--out review.json]"))),
		cli.WithSubcommand(cli.NewCommand("create", "Admit the saved reviewed request", a.createNativeCopy,
			cli.WithFlags(append(recoveryFlags(), cli.NewStringFlag("review-file", "", "Saved native-copy review JSON file", ""), cli.NewBoolFlag("acknowledge-charges", "", "Acknowledge charges until confirmed deletion, including failed cleanup", false))...),
			cli.WithUsage("backups native-copies create --review-file review.json --acknowledge-charges [--wait] [--json]"))),
		cli.WithSubcommand(cli.NewCommand("show", "Show retained native copy and cleanup status", a.showNativeCopy, cli.WithFlags(recoveryFlags()...), cli.WithUsage("backups native-copies show <copy> [--wait] [--json]"))),
		cli.WithSubcommand(cli.NewCommand("credentials", "Save freshly checked reader credentials to a private file", a.nativeCopyCredentials, cli.WithFlag(cli.NewStringFlag("out", "", "New owner-only credential JSON file", "")), cli.WithUsage("backups native-copies credentials <copy> --out copy-credentials.json"))),
		cli.WithSubcommand(cli.NewCommand("cleanup", "Request removal of this temporary copy", a.cleanupNativeCopy,
			cli.WithFlags(append(recoveryFlags(), cli.NewBoolFlag("yes", "y", "Confirm removal of the temporary copy", false))...), cli.WithUsage("backups native-copies cleanup <copy> --yes [--wait] [--json]"))))
}

func (a *App) reviewNativeCopy(ctx cli.CommandContext) error {
	ttl, err := strconv.Atoi(ctx.String("ttl-hours"))
	if err != nil || ttl < 1 || ttl > 24 || ctx.Arg(0) == "" || ctx.Arg(1) == "" || ctx.String("cloud") == "" || ctx.String("target") == "" {
		return fmt.Errorf("provide source, point, granted Cloud, target and ttl-hours from 1 to 24")
	}
	var restoreTime *time.Time
	if raw := ctx.String("restore-time"); raw != "" {
		at, parseErr := time.Parse(time.RFC3339Nano, raw)
		if parseErr != nil || !strings.HasSuffix(raw, "Z") || at.Nanosecond() != 0 {
			return fmt.Errorf("restore-time must be an explicit whole UTC second such as 2026-10-22T12:00:03Z")
		}
		restoreTime = &at
	}
	c, err := a.client()
	if err != nil {
		return err
	}
	review, err := c.ReviewNativeRecoveryCopy(ctxOf(ctx), ctx.Arg(0), ctx.Arg(1), client.NativeCopyReviewRequest{RestoreTime: restoreTime, RequestID: requestID(ctx), CloudID: ctx.String("cloud"), TargetID: ctx.String("target"), TTLHours: ttl})
	if err != nil {
		return err
	}
	if path := ctx.String("out"); path != "" {
		raw, marshalErr := json.MarshalIndent(review, "", "  ")
		if marshalErr != nil {
			return marshalErr
		}
		file, openErr := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if openErr != nil {
			return openErr
		}
		_, writeErr := file.Write(append(raw, '\n'))
		if saveErr := errors.Join(writeErr, file.Close()); saveErr != nil {
			return saveErr
		}
		if !ctx.Bool("json") {
			nativeAccount := review.Target.NativeProject
			if nativeAccount == "" {
				nativeAccount = review.Target.NativeAccount
			}
			if review.RestoreTime != nil {
				a.printf("PITR time: %s\n", review.RestoreTime.UTC().Format(time.RFC3339Nano))
			}
			a.printf("Target %s/%s: %s in %s. %s, %d GiB %s, lifetime %dh. Review expires %s.\n", review.Target.CloudID, review.Target.TargetID, nativeAccount, review.Target.Region, review.Capacity.ComputeClass, review.Capacity.StorageGB, review.Capacity.StorageType, review.TTLHours, review.ValidUntil.UTC().Format("2006-01-02 15:04Z"))
			a.printf("Saved request %s to %s. %s\n", review.RequestID, path, review.Notice)
			return nil
		}
	}
	return a.json(review)
}

func (a *App) createNativeCopy(ctx cli.CommandContext) error {
	if !ctx.Bool("acknowledge-charges") || ctx.String("review-file") == "" {
		return fmt.Errorf("provide --review-file and --acknowledge-charges")
	}
	file, err := os.Open(ctx.String("review-file"))
	if err != nil {
		return err
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, (64<<10)+1))
	if err != nil {
		return err
	}
	if len(raw) > 64<<10 {
		return fmt.Errorf("native review exceeds 64 KiB")
	}
	var review client.NativeRecoveryReview
	if err = json.Unmarshal(raw, &review); err != nil {
		return fmt.Errorf("read native review: %w", err)
	}
	if review.RequestID == "" || review.ReviewToken == "" {
		return fmt.Errorf("review requires request_id and review_token")
	}
	c, err := a.client()
	if err != nil {
		return err
	}
	record, err := c.CreateNativeRecoveryCopy(ctxOf(ctx), client.NativeCopyRequest{RequestID: review.RequestID, ReviewToken: review.ReviewToken, AcknowledgeCharges: true})
	if err != nil {
		return fmt.Errorf("native copy request %s: %w", review.RequestID, err)
	}
	return a.nativeCopyResult(ctx, c, record)
}

func (a *App) showNativeCopy(ctx cli.CommandContext) error {
	if ctx.Arg(0) == "" {
		return fmt.Errorf("give a native copy ID")
	}
	c, err := a.client()
	if err != nil {
		return err
	}
	record, err := c.NativeRecoveryCopy(ctxOf(ctx), ctx.Arg(0))
	if err != nil {
		return err
	}
	return a.nativeCopyResult(ctx, c, record)
}

func (a *App) cleanupNativeCopy(ctx cli.CommandContext) error {
	if ctx.Arg(0) == "" || !ctx.Bool("yes") {
		return fmt.Errorf("give a native copy ID and --yes")
	}
	c, err := a.client()
	if err != nil {
		return err
	}
	record, err := c.CleanupNativeRecoveryCopy(ctxOf(ctx), ctx.Arg(0))
	if err != nil {
		return err
	}
	return a.nativeCopyResult(ctx, c, record)
}

func (a *App) nativeCopyResult(ctx cli.CommandContext, c *client.Client, record *client.NativeRecoveryCopy) error {
	bounded, cancel := recoveryContext(ctx)
	defer cancel()
	if ctx.Bool("wait") {
		latest, err := c.WaitNativeRecoveryCopy(bounded, record.ID)
		if latest != nil {
			record = latest
		}
		if err != nil {
			if ctx.Bool("json") {
				err = errors.Join(err, a.json(record))
			}
			return fmt.Errorf("follow native copy %s with loomctl backups native-copies show: %w", record.ID, err)
		}
	}
	return a.recoveryResult(ctx, record, record.ID, record.Status, record.Error)
}

func (a *App) nativeCopies(ctx cli.CommandContext) error {
	limit, err := nativeRecoveryLimit(ctx)
	if err != nil {
		return err
	}
	c, err := a.client()
	if err != nil {
		return err
	}
	project, err := a.project(ctx, c)
	if err != nil {
		return err
	}
	page, err := c.NativeRecoveryCopies(ctxOf(ctx), project.ID, client.NativeCopyPage{EnvironmentID: ctx.String("environment-id"), Resource: ctx.String("resource"), Before: ctx.String("before"), Limit: limit})
	if err != nil {
		return err
	}
	if ctx.Bool("json") {
		return a.json(page)
	}
	rows := [][]string{}
	for _, record := range page.Copies {
		verified := "not verified"
		if record.VerifiedAt != nil {
			verified = record.VerifiedAt.UTC().Format("2006-01-02 15:04Z")
		}
		rows = append(rows, []string{record.ID, record.SourceID, record.Target.Provider, record.Status, record.ExpiresAt.UTC().Format("2006-01-02 15:04Z"), verified, record.Error})
	}
	a.table([]string{"COPY", "SOURCE", "PROVIDER", "STATUS", "EXPIRES", "SQL VERIFIED", "ERROR"}, rows)
	if page.NextBefore != "" {
		a.printf("Next page: --before %s\n", page.NextBefore)
	}
	return nil
}
