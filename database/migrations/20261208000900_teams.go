package migrations

import (
	"context"
	"fmt"

	"github.com/xraph/grove/migrate"
)

// Workspace teams: named groups of members that carry a role per project or
// for the whole workspace, a project's responsible team, and the queue of
// people whose Warden assignments need recomputing.
//
// These tables are Loomplane's own. authsome's authsome_teams stays unused
// and Loomplane blocks its routes. A row in loomplane_team_members names the
// authsome member it was added for (member_id), and grants nothing unless
// that member row still exists. A person who leaves and is invited back gets
// a new member ID, so a membership row that survived their leaving matches
// nothing and gives them no team rights.
//
// A grant with a NULL project_id applies to the whole workspace; at most one
// grant exists per team and project, and at most one workspace grant per
// team. The queue rows are keyed by person, so queueing twice keeps one row.
func init() {
	App.Add(
		func(ctx context.Context, db *migrate.DB) error {
			err := db.NewCreateTable("loomplane_teams").IfNotExists().
				Column("id", migrate.Text, migrate.PrimaryKey()).
				Column("org_id", migrate.Text, migrate.NotNull()).
				Column("name", migrate.Text, migrate.NotNull(), migrate.Check("char_length(name) BETWEEN 1 AND 80")).
				Column("slug", migrate.Text, migrate.NotNull()).
				Column("description", migrate.Text, migrate.NotNull(), migrate.Default(""),
					migrate.Check("char_length(description) <= 500")).
				Column("created_by", migrate.Text, migrate.NotNull()).
				Column("created_at", migrate.Timestamp, migrate.NotNull(), migrate.DefaultExpr("now()")).
				Column("updated_at", migrate.Timestamp, migrate.NotNull(), migrate.DefaultExpr("now()")).
				Unique("org_id", "slug").Named("loomplane_teams_slug").
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewCreateTable("loomplane_team_members").IfNotExists().
				Column("team_id", migrate.Text, migrate.NotNull(),
					migrate.References("loomplane_teams", "id"), migrate.OnDelete(migrate.Cascade)).
				Column("org_id", migrate.Text, migrate.NotNull()).
				Column("user_id", migrate.Text, migrate.NotNull()).
				Column("member_id", migrate.Text, migrate.NotNull()).
				Column("added_by", migrate.Text, migrate.NotNull()).
				Column("added_at", migrate.Timestamp, migrate.NotNull(), migrate.DefaultExpr("now()")).
				PrimaryKey("team_id", "user_id").
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewCreateIndex("loomplane_team_members_user").IfNotExists().
				On("loomplane_team_members").
				Columns("org_id", "user_id").
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewCreateTable("loomplane_team_grants").IfNotExists().
				Column("id", migrate.Text, migrate.PrimaryKey()).
				Column("team_id", migrate.Text, migrate.NotNull(),
					migrate.References("loomplane_teams", "id"), migrate.OnDelete(migrate.Cascade)).
				Column("org_id", migrate.Text, migrate.NotNull()).
				Column("role", migrate.Text, migrate.NotNull(), migrate.Check("role IN ('viewer','developer','admin')")).
				Column("project_id", migrate.Text,
					migrate.References("loomplane_projects", "id"), migrate.OnDelete(migrate.Cascade)).
				Column("created_by", migrate.Text, migrate.NotNull()).
				Column("created_at", migrate.Timestamp, migrate.NotNull(), migrate.DefaultExpr("now()")).
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewCreateIndex("loomplane_team_grants_scope").IfNotExists().
				On("loomplane_team_grants").
				Columns("team_id").
				ColumnExpr("coalesce(project_id, '')").
				Unique().
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewCreateIndex("loomplane_team_grants_project").IfNotExists().
				On("loomplane_team_grants").
				Columns("project_id").
				Where("project_id IS NOT NULL").
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewAlterTable("loomplane_projects").
				AddColumn("responsible_team_id", migrate.Text,
					migrate.References("loomplane_teams", "id"), migrate.OnDelete(migrate.SetNull)).IfNotExists().
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewCreateTable("loomplane_authz_sync").IfNotExists().
				Column("org_id", migrate.Text, migrate.NotNull()).
				Column("user_id", migrate.Text, migrate.NotNull()).
				Column("queued_at", migrate.Timestamp, migrate.NotNull(), migrate.DefaultExpr("clock_timestamp()")).
				PrimaryKey("org_id", "user_id").
				Exec(ctx)
			if err != nil {
				return err
			}

			if _, err = db.Exec(ctx, planNoticeFunctionTeams); err != nil {
				return fmt.Errorf("create function loomplane_plan_notice: %w", err)
			}
			return nil
		},
		func(ctx context.Context, db *migrate.DB) error {
			// The function reads the tables below, so it goes back to the
			// 0300 text before they're dropped.
			if _, err := db.Exec(ctx, planNoticeFunction0300); err != nil {
				return fmt.Errorf("restore function loomplane_plan_notice: %w", err)
			}
			err := db.NewAlterTable("loomplane_projects").
				DropColumn("responsible_team_id").IfExists().
				Exec(ctx)
			if err != nil {
				return err
			}
			for _, table := range []string{"loomplane_authz_sync", "loomplane_team_grants", "loomplane_team_members", "loomplane_teams"} {
				if err := db.NewDropTable(table).IfExists().Exec(ctx); err != nil {
					return err
				}
			}
			return nil
		},
	)
}

// planNoticeFunctionTeams is loomplane_plan_notice() (from 20261205000300)
// with teams added to who hears that a plan is waiting: owner and admin
// members as before, members of a team with an admin grant on the
// workspace or on the plan's project, and members of the project's
// responsible team, never the plan's creator. A team member counts only
// while the authsome membership they were added for still exists (the
// member ID, never timestamps), only in the plan's org, and only if they're
// a person (an ID with a colon is automation or the agent). Delivery still
// rechecks deployment:approve on the project, so a responsible-team member
// who can't approve gets no inbox item. Everything else (the delay,
// plan.decided, the exception block) is 0300's.
//
// This function now has two owners, 0300 and this migration. Any later
// rewrite (push 6, for one) must keep the team union.
const planNoticeFunctionTeams = `CREATE OR REPLACE FUNCTION loomplane_plan_notice() RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
	IF OLD.status='planning' AND NEW.status='ready' THEN
		INSERT INTO loomplane_notification_outbox(id,org_id,project_id,user_id,deployment_id,attempt,payload,kind,subject_id,inbox_done,webhook_done,email_state,next_attempt_at)
		SELECT 'plan.waiting:'||NEW.id||':'||m.user_id,NEW.org_id,NEW.project_id,m.user_id,'plan.waiting:'||NEW.id||':'||m.user_id,0,
			jsonb_build_object('plan_id',NEW.id,'project_id',NEW.project_id,'environment_id',NEW.environment_id,'environment',NEW.environment,'revision',NEW.revision),
			'plan.waiting',NEW.id,FALSE,TRUE,'pending',
			CASE WHEN NEW.created_by LIKE 'loomplane:%' THEN clock_timestamp() ELSE clock_timestamp()+interval '2 minutes' END
		FROM (SELECT DISTINCT r.user_id FROM (
			SELECT m.user_id FROM authsome_members m WHERE m.org_id=NEW.org_id AND m.role IN ('owner','admin')
			UNION SELECT tm.user_id FROM loomplane_team_members tm
				JOIN loomplane_team_grants g ON g.team_id=tm.team_id AND g.role='admin' AND (g.project_id IS NULL OR g.project_id=NEW.project_id)
				JOIN authsome_members m ON m.id=tm.member_id AND m.org_id=tm.org_id AND m.user_id=tm.user_id
				WHERE tm.org_id=NEW.org_id AND position(':' IN tm.user_id)=0
			UNION SELECT tm.user_id FROM loomplane_projects p JOIN loomplane_team_members tm ON tm.team_id=p.responsible_team_id
				JOIN authsome_members m ON m.id=tm.member_id AND m.org_id=tm.org_id AND m.user_id=tm.user_id
				WHERE p.id=NEW.project_id AND tm.org_id=NEW.org_id AND position(':' IN tm.user_id)=0
		) r WHERE r.user_id<>NEW.created_by) m
		ON CONFLICT(deployment_id,attempt) DO NOTHING;
	ELSIF OLD.status='ready' THEN
		INSERT INTO loomplane_notification_outbox(id,org_id,project_id,user_id,deployment_id,attempt,payload,kind,subject_id,inbox_done,webhook_done,email_state)
		SELECT 'plan.decided:'||NEW.id||':'||w.user_id,NEW.org_id,NEW.project_id,w.user_id,'plan.decided:'||NEW.id||':'||w.user_id,0,
			jsonb_build_object('plan_id',NEW.id,'project_id',NEW.project_id,'environment_id',NEW.environment_id,'environment',NEW.environment,'revision',NEW.revision,'status',NEW.status,'decided_by',NEW.decided_by),
			'plan.decided',NEW.id,TRUE,TRUE,'skipped'
		FROM loomplane_notification_outbox w WHERE w.kind='plan.waiting' AND w.subject_id=NEW.id
		ON CONFLICT(deployment_id,attempt) DO NOTHING;
	END IF;
	RETURN NULL;
EXCEPTION WHEN OTHERS THEN
	-- A notice must never fail the plan write that caused it, but a dropped
	-- notice leaves a trace. Error text and code only, never row data.
	RAISE WARNING 'loomplane_plan_notice failed: % (SQLSTATE %)', SQLERRM, SQLSTATE;
	RETURN NULL;
END $$`
