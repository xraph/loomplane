package ops

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/xraph/forge/cli"
	"github.com/xraph/grove/migrate"

	"github.com/xraph/loomplane/internal/platformadmin"
)

// Settings are what the commands need: the database, the schema this
// Loomplane has, and its version.
type Settings struct {
	DatabaseURL string
	Groups      []*migrate.Group
	Version     string
	// AuthAppID is Loomplane's authsome app, where platform roles live.
	AuthAppID string
	// SecretKey and PublicURL give the one-time setup link.
	SecretKey string
	PublicURL string
}

// Commands are loomplane's operational commands. settings is called when
// a command runs, so loading the configuration can fail there, with the
// command's own error.
func Commands(settings func() (Settings, error)) []cli.Command {
	return []cli.Command{
		cli.NewCommand("backup", "Back up Loomplane's database to a file", func(ctx cli.CommandContext) error {
			s, err := settings()
			if err != nil {
				return err
			}
			path := ctx.String("out")
			if path == "" {
				path = "loomplane-" + time.Now().UTC().Format("20060102-150405") + ".tar.gz"
			}
			// Written beside its final name, and renamed once complete, so a
			// failed backup never leaves something that looks like one.
			tmp := path + ".partial"
			f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
			if err != nil {
				return err
			}
			m, err := Backup(ctx.Context(), s.DatabaseURL, s.Version, f)
			if cerr := f.Close(); err == nil {
				err = cerr
			}
			if err != nil {
				_ = os.Remove(tmp)
				return err
			}
			if err := os.Rename(tmp, path); err != nil {
				return err
			}
			var rows int64
			for _, t := range m.Tables {
				rows += t.Rows
			}
			fmt.Printf("Backed up %d tables, %d rows, to %s.\nKeep secret_key safe as well: without it, the secrets in this backup can't be read.\n", len(m.Tables), rows, path)
			return nil
		},
			cli.WithUsage("loomplane backup [--out file.tar.gz]"),
			cli.WithFlag(cli.NewStringFlag("out", "o", "Where to write it; loomplane-<time>.tar.gz if empty", "")),
			cli.WithSubcommand(cli.NewCommand("inspect", "Show what a backup holds, without restoring it", func(ctx cli.CommandContext) error {
				f, err := os.Open(ctx.Arg(0))
				if err != nil {
					return err
				}
				defer f.Close()
				m, err := Inspect(f)
				if err != nil {
					return err
				}
				fmt.Printf("Made by Loomplane %s at %s, with %d migrations applied.\n", m.Version, m.CreatedAt.Format(time.RFC3339), len(m.Migrations))
				for _, t := range m.Tables {
					fmt.Printf("  %-48s %d rows\n", t.Name, t.Rows)
				}
				return nil
			}, cli.WithUsage("loomplane backup inspect <file>"))),
		),
		cli.NewCommand("restore", "Restore Loomplane's database from a backup", func(ctx cli.CommandContext) error {
			s, err := settings()
			if err != nil {
				return err
			}
			f, err := os.Open(ctx.Arg(0))
			if err != nil {
				return err
			}
			defer f.Close()
			m, err := Restore(ctx.Context(), s.DatabaseURL, s.Groups, f, RestoreOptions{Force: ctx.Bool("force")})
			if err != nil {
				return err
			}
			fmt.Printf("Restored the backup Loomplane %s made at %s: %d tables.\nStart Loomplane with the secret_key it ran with.\n", m.Version, m.CreatedAt.Format(time.RFC3339), len(m.Tables))
			return nil
		},
			cli.WithUsage("loomplane restore <file> [--force]; stop Loomplane first"),
			cli.WithFlag(cli.NewBoolFlag("force", "", "Replace a database that already has data", false)),
		),
		cli.NewCommand("migrations", "Show which migrations the database has and which are pending", func(ctx cli.CommandContext) error {
			s, err := settings()
			if err != nil {
				return err
			}
			st, err := SchemaStatus(ctx.Context(), s.DatabaseURL, s.Groups)
			if err != nil {
				return err
			}
			fmt.Printf("%d applied, %d pending.\n", len(st.Applied), len(st.Pending))
			for _, p := range st.Pending {
				fmt.Println("  pending:", p)
			}
			if len(st.Unknown) > 0 {
				return fmt.Errorf("%w: %s", ErrNewerSchema, strings.Join(st.Unknown, ", "))
			}
			if ctx.Bool("check") && len(st.Pending) > 0 {
				return fmt.Errorf("%w: %d pending", ErrPendingMigrations, len(st.Pending))
			}
			return nil
		},
			cli.WithUsage("loomplane migrations [--check]"),
			// The Kubernetes chart's worker waits on this, since only the
			// API migrates: until loomplane migrations --check; do sleep 5; done
			cli.WithFlag(cli.NewBoolFlag("check", "", "Exit non-zero while any migration is still pending", false)),
		),
		cli.NewCommand("migrate", "Apply pending migrations; starting Loomplane does this too", func(ctx cli.CommandContext) error {
			s, err := settings()
			if err != nil {
				return err
			}
			if err := CheckCompatible(ctx.Context(), s.DatabaseURL, s.Groups); err != nil {
				return err
			}
			if err := Migrate(ctx.Context(), s.DatabaseURL, s.Groups); err != nil {
				return err
			}
			fmt.Println("The database is up to date.")
			return nil
		}),
		cli.NewCommand("setup-token", "Print the link that finishes first-run setup", func(ctx cli.CommandContext) error {
			s, err := settings()
			if err != nil {
				return err
			}
			out, err := SetupTokenOutput(ctx.Context(), s, ctx.Bool("raw"))
			if err != nil {
				return err
			}
			fmt.Println(out)
			return nil
		},
			cli.WithUsage("loomplane setup-token [--raw]"),
			cli.WithFlag(cli.NewBoolFlag("raw", "", "Print the token alone, for scripts", false)),
		),
		cli.NewCommand("admin", "Manage who runs this Loomplane instance", func(cli.CommandContext) error {
			return errors.New("use a subcommand: loomplane admin grant-platform-admin <email>")
		},
			cli.WithUsage("loomplane admin grant-platform-admin <email>"),
			cli.WithSubcommand(cli.NewCommand("grant-platform-admin", "Let an existing user open the instance console", func(ctx cli.CommandContext) error {
				email := strings.TrimSpace(ctx.Arg(0))
				if email == "" {
					return errors.New("usage: loomplane admin grant-platform-admin <email>")
				}
				s, err := settings()
				if err != nil {
					return err
				}
				users, roles, closeDB, err := platformadmin.Open(ctx.Context(), s.DatabaseURL, s.AuthAppID)
				if err != nil {
					return err
				}
				defer closeDB()
				granted, err := platformadmin.Grant(ctx.Context(), users, roles, s.AuthAppID, email)
				if errors.Is(err, platformadmin.ErrNoUser) {
					return fmt.Errorf("no user with email %s", email)
				}
				if err != nil {
					return err
				}
				if !granted {
					fmt.Printf("%s is already a platform admin.\n", email)
					return nil
				}
				fmt.Printf("Granted platform-owner to %s.\n", email)
				return nil
			}, cli.WithUsage("loomplane admin grant-platform-admin <email>"))),
		),
	}
}
