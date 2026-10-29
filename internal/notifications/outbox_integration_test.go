//go:build integration

package notifications

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/grove"
	"github.com/xraph/grove/drivers/pgdriver"
	"github.com/xraph/grove/migrate"
	"github.com/xraph/herald/message"
	"github.com/xraph/relay"
	"github.com/xraph/relay/catalog"
	"github.com/xraph/relay/delivery"
	"github.com/xraph/relay/signature"

	"github.com/xraph/loomplane/database/migrations"
	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/security"
)

type allowedProjects struct{ denied atomic.Bool }

func (a *allowedProjects) Require(_ context.Context, _ security.Principal, permission security.Permission, _ string) error {
	if a.denied.Load() && permission == security.ProjectRead {
		return security.ErrForbidden
	}
	return nil
}

func notificationDatabase(t *testing.T) *grove.DB {
	t.Helper()
	dsn := os.Getenv("LOOMPLANE_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set LOOMPLANE_TEST_DATABASE_URL for notifications PostgreSQL tests")
	}
	ctx := t.Context()
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("notification_%d", time.Now().UnixNano())
	if _, err = admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), "DROP DATABASE "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)")
		_ = admin.Close(context.Background())
	})
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	pg := pgdriver.New()
	if err = pg.Open(ctx, u.String()); err != nil {
		t.Fatal(err)
	}
	db, err := grove.Open(pg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	for _, q := range []string{
		`CREATE TABLE authsome_users(id TEXT PRIMARY KEY,app_id TEXT NOT NULL,email TEXT NOT NULL,first_name TEXT NOT NULL DEFAULT '',last_name TEXT NOT NULL DEFAULT '',username TEXT NOT NULL DEFAULT '',display_username TEXT NOT NULL DEFAULT '',banned BOOLEAN NOT NULL DEFAULT false,deleted_at TIMESTAMPTZ)`,
		`CREATE TABLE authsome_members(org_id TEXT NOT NULL,user_id TEXT NOT NULL,role TEXT NOT NULL DEFAULT 'admin')`,
		`CREATE TABLE loomplane_projects(id TEXT PRIMARY KEY,org_id TEXT NOT NULL,name TEXT NOT NULL DEFAULT '')`,
		`CREATE TABLE loomplane_environments(id TEXT PRIMARY KEY,org_id TEXT NOT NULL,project_id TEXT NOT NULL,name TEXT NOT NULL DEFAULT '',kind TEXT NOT NULL DEFAULT 'staging')`,
		`CREATE TABLE authsome_organizations(id TEXT PRIMARY KEY,name TEXT NOT NULL DEFAULT '')`,
		`CREATE TABLE loomplane_deployments(id TEXT PRIMARY KEY,org_id TEXT NOT NULL,project_id TEXT NOT NULL,environment_id TEXT NOT NULL,plan_id TEXT NOT NULL,started_by TEXT NOT NULL,status TEXT NOT NULL,retries INT NOT NULL DEFAULT 0,error TEXT NOT NULL DEFAULT '')`,
		`INSERT INTO authsome_users(id,app_id,email)VALUES('user-a','app','user-a@example.com'),('user-b','app','user-b@example.com')`,
		`INSERT INTO authsome_members(org_id,user_id)VALUES('org-a','user-a'),('org-b','user-b')`,
		`INSERT INTO loomplane_projects(id,org_id)VALUES('project-a','org-a'),('project-b','org-b')`,
	} {
		if _, err = pg.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	executor, err := migrate.NewExecutorFor(pg)
	if err != nil {
		t.Fatal(err)
	}
	if err = migrations.NotificationsMigration().Up(ctx, executor); err != nil {
		t.Fatal(err)
	}
	// The cloud_sign_in and notification_kinds migrations both add the
	// outbox's kind column. This helper only runs the notifications
	// migration, so it runs notification_kinds as well, then
	// generic_notices, which adds the subject and the agent email switch,
	// then push_outbox, which adds the push columns.
	for _, m := range migrations.Group().Migrations() {
		if m.Name == "notification_kinds" || m.Name == "generic_notices" || m.Name == "push_outbox" {
			if err = m.Up(ctx, executor); err != nil {
				t.Fatal(err)
			}
		}
	}
	return db
}

// cloudManagers grants organization cloud management to the listed users
// only, and everything else to everyone.
type cloudManagers map[string]bool

func (c cloudManagers) Require(_ context.Context, p security.Principal, permission security.Permission, resource string) error {
	if permission == security.OrganizationCloudManage && (!c[p.UserID] || resource != p.OrgID) {
		return security.ErrForbidden
	}
	return nil
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestPostgresOutboxReplaysWebhookOnceAndKeepsInboxScoped(t *testing.T) {
	db := notificationDatabase(t)
	ctx := t.Context()
	cfg := config.Config{PublicURL: "https://loomplane.example.com", SecretKey: base64.StdEncoding.EncodeToString(make([]byte, 32)), Identity: config.IdentityConfig{AppID: "app"}}
	svc, err := New(cfg, db)
	if err != nil {
		t.Fatal(err)
	}
	if err = svc.mailStore.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err = svc.relayStore.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	authz := &allowedProjects{}
	svc.SetAuthorizer(authz)
	var requests atomic.Int32
	var valid atomic.Bool
	var webhookSecret string
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		timestamp, _ := strconv.ParseInt(r.Header.Get("X-Relay-Timestamp"), 10, 64)
		if signature.Verify(body, webhookSecret, timestamp, r.Header.Get("X-Relay-Signature")) {
			valid.Store(true)
		}
		if requests.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer receiver.Close()
	target, _ := url.Parse(receiver.URL)
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		copyRequest := r.Clone(r.Context())
		copyURL := *r.URL
		copyURL.Scheme, copyURL.Host = target.Scheme, target.Host
		copyRequest.URL = &copyURL
		return http.DefaultTransport.RoundTrip(copyRequest)
	})
	svc.relay, err = relay.New(relay.WithStore(svc.relayStore), relay.WithHTTPClient(&http.Client{Transport: transport}), relay.WithPollInterval(10*time.Millisecond), relay.WithRetrySchedule([]time.Duration{10 * time.Millisecond}))
	if err != nil {
		t.Fatal(err)
	}
	p := security.Principal{OrgID: "org-a", UserID: "user-a"}
	hook, err := svc.CreateEndpoint(ctx, p, WebhookEndpointInput{URL: "https://8.8.8.8/hook", Description: "Integration test"})
	if err != nil {
		t.Fatal(err)
	}
	webhookSecret = hook.Secret
	if err = svc.Prepare(ctx); err != nil {
		t.Fatal(err)
	}
	if err = svc.Run(ctx); err != nil {
		t.Fatal(err)
	}

	if _, err = svc.pg.Exec(ctx, `INSERT INTO loomplane_deployments(id,org_id,project_id,environment_id,plan_id,started_by,status,error)VALUES('deployment-a','org-a','project-a','env-a','plan-a','user-a','running','DO_NOT_SEND_SECRET')`); err != nil {
		t.Fatal(err)
	}
	// A rolled-back terminal state cannot enqueue notifications.
	tx, err := svc.pg.BeginTxQuery(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.NewRaw(`UPDATE loomplane_deployments SET status='succeeded' WHERE id='deployment-a'`).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = svc.pg.QueryRow(ctx, `SELECT count(*) FROM loomplane_notification_outbox`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("rolled-back completion queued: %d %v", count, err)
	}
	if _, err = svc.pg.Exec(ctx, `UPDATE loomplane_deployments SET status='succeeded' WHERE id='deployment-a'`); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var complete bool
		if err = svc.pg.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM loomplane_notification_outbox WHERE completed_at IS NOT NULL)`).Scan(&complete); err != nil {
			t.Fatal(err)
		}
		if complete && requests.Load() >= 2 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if requests.Load() != 2 || !valid.Load() {
		t.Fatalf("signed retry not delivered: requests=%d signature=%t", requests.Load(), valid.Load())
	}
	list, err := svc.List(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 1 || list.Items[0].EmailStatus != "skipped" {
		t.Fatalf("inbox: %+v", list)
	}
	other := security.Principal{OrgID: "org-b", UserID: "user-b"}
	if err = svc.MarkRead(ctx, other, list.Items[0].ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-org inbox mutation: %v", err)
	}
	if _, err = svc.RotateEndpoint(ctx, other, hook.Endpoint.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-org secret rotation: %v", err)
	}
	deliveries, err := svc.Deliveries(ctx, p, hook.Endpoint.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(deliveries.Items) != 1 || deliveries.Items[0].Status != string(delivery.StateDelivered) || deliveries.Items[0].Attempts != 2 {
		t.Fatalf("delivery history: %+v", deliveries)
	}
	// Stop the entire service and instantiate fresh engines over the same DB,
	// as happens after a process restart. The committed receipt must survive.
	if err = svc.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	svc, err = New(cfg, db)
	if err != nil {
		t.Fatal(err)
	}
	svc.SetAuthorizer(authz)
	svc.relay, err = relay.New(relay.WithStore(svc.relayStore), relay.WithHTTPClient(&http.Client{Transport: transport}))
	if err != nil {
		t.Fatal(err)
	}
	if err = svc.Prepare(ctx); err != nil {
		t.Fatal(err)
	}
	// Simulate a crash after Relay committed acceptance, before checkpointing.
	if _, err = svc.pg.Exec(ctx, `UPDATE loomplane_notification_outbox SET webhook_done=false,completed_at=NULL,lease_until=NULL,next_attempt_at=clock_timestamp()`); err != nil {
		t.Fatal(err)
	}
	if err = svc.drain(ctx); err != nil {
		t.Fatal(err)
	}
	var receipts int
	if err = svc.pg.QueryRow(ctx, `SELECT count(*) FROM relay_acceptances`).Scan(&receipts); err != nil {
		t.Fatal(err)
	}
	if receipts != 1 || requests.Load() != 2 {
		t.Fatalf("replay duplicated webhook: receipts=%d requests=%d", receipts, requests.Load())
	}
	var replayComplete bool
	if err = svc.pg.QueryRow(ctx, `SELECT completed_at IS NOT NULL FROM loomplane_notification_outbox WHERE deployment_id='deployment-a'`).Scan(&replayComplete); err != nil || !replayComplete {
		t.Fatalf("replayed acceptance did not complete its outbox checkpoint: complete=%t err=%v", replayComplete, err)
	}
	list, err = svc.List(ctx, p)
	if err != nil || len(list.Items) != 1 {
		t.Fatalf("replay duplicated inbox: %+v %v", list, err)
	}
	authz.denied.Store(true)
	list, err = svc.List(ctx, p)
	if err != nil || len(list.Items) != 0 {
		t.Fatalf("revoked project retained inbox access: %+v %v", list, err)
	}
	// Retry creates a new stable attempt and carries only the allowlisted fields.
	if _, err = svc.pg.Exec(ctx, `UPDATE loomplane_deployments SET status='running',retries=1 WHERE id='deployment-a'`); err != nil {
		t.Fatal(err)
	}
	if _, err = svc.pg.Exec(ctx, `UPDATE loomplane_deployments SET status='failed' WHERE id='deployment-a'`); err != nil {
		t.Fatal(err)
	}
	var raw []byte
	if err = svc.pg.QueryRow(ctx, `SELECT payload FROM loomplane_notification_outbox WHERE attempt=1`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err = json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "DO_NOT_SEND_SECRET") || len(payload) != 6 {
		t.Fatalf("unexpected outbound fields: %s", raw)
	}
}

// smtpSink is an actual SMTP peer. It records DATA bytes while the real Herald
// SMTP driver performs its greeting, envelope, DATA and quit conversation.
func smtpSink(t *testing.T) (string, int, <-chan string) {
	t.Helper()
	var listenConfig net.ListenConfig
	listener, err := listenConfig.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	messages := make(chan string, 10)
	var wg sync.WaitGroup
	t.Cleanup(func() { _ = listener.Close(); wg.Wait() })
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			conn, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
				_, _ = io.WriteString(conn, "220 smtp-test ESMTP\r\n")
				scan := bufio.NewScanner(conn)
				for scan.Scan() {
					line := scan.Text()
					switch {
					case strings.HasPrefix(line, "EHLO"), strings.HasPrefix(line, "HELO"):
						_, _ = io.WriteString(conn, "250 smtp-test\r\n")
					case strings.HasPrefix(line, "MAIL"), strings.HasPrefix(line, "RCPT"):
						_, _ = io.WriteString(conn, "250 ok\r\n")
					case line == "DATA":
						_, _ = io.WriteString(conn, "354 send data\r\n")
						var body strings.Builder
						for scan.Scan() {
							if scan.Text() == "." {
								break
							}
							body.WriteString(scan.Text() + "\n")
						}
						messages <- body.String()
						_, _ = io.WriteString(conn, "250 accepted\r\n")
					case line == "QUIT":
						_, _ = io.WriteString(conn, "221 bye\r\n")
						return
					default:
						_, _ = io.WriteString(conn, "250 ok\r\n")
					}
				}
			}()
		}
	}()
	host, portText, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	return host, port, messages
}
func TestRealSMTPInvitationRedactsPersistedCredentialMail(t *testing.T) {
	db := notificationDatabase(t)
	host, port, messages := smtpSink(t)
	ctx := t.Context()
	cfg := config.Config{PublicURL: "https://loomplane.example.com", SecretKey: base64.StdEncoding.EncodeToString(make([]byte, 32)), Identity: config.IdentityConfig{AppID: "app"}, Notifications: config.NotificationsConfig{SMTPHost: host, SMTPPort: port, From: "loomplane@example.com"}}
	svc, err := New(cfg, db)
	if err != nil {
		t.Fatal(err)
	}
	if err = svc.mailStore.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err = svc.Prepare(ctx); err != nil {
		t.Fatal(err)
	}
	if err = svc.SendInvitation(ctx, "org-a", "recipient@example.com", "https://loomplane.example.com/invitations/accept?token=PRIVATE_INVITE_TOKEN", "Test org", "developer"); err != nil {
		t.Fatal(err)
	}
	select {
	case body := <-messages:
		if !strings.Contains(body, "PRIVATE_INVITE_TOKEN") || !strings.Contains(body, "recipient@example.com") {
			t.Fatal("SMTP message lost invitation link or envelope")
		}
	case <-time.After(time.Second):
		t.Fatal("SMTP invitation was not delivered")
	}
	logs, err := svc.mailStore.ListMessages(ctx, "app", message.ListOptions{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(logs) != 1 || logs[0].Status != message.StatusSent || logs[0].Body != "" || logs[0].Recipient != "" || len(logs[0].Metadata) > 0 {
		t.Fatalf("security content retained in log: %#v", logs)
	}
	svc.cfg.Notifications.SMTPUsername = "smtp-user"
	svc.cfg.Notifications.SMTPPassword = "smtp-password"
	if err = svc.Prepare(ctx); err != nil {
		t.Fatal(err)
	}
	providers, err := svc.mailStore.ListAllProviders(ctx, "app")
	if err != nil {
		t.Fatal(err)
	}
	if len(providers) != 1 {
		t.Fatal("SMTP configuration duplicated its provider")
	}
	if password := providers[0].Credentials["password"]; !strings.HasPrefix(password, "enc:v1:lp1:") || strings.Contains(password, "smtp-password") {
		t.Fatal("SMTP password not encrypted at rest")
	}
}

func TestPostgresEmailExhaustionRecoveryAndDeliveryTimeOptOut(t *testing.T) {
	db := notificationDatabase(t)
	ctx := t.Context()
	cfg := config.Config{PublicURL: "https://loomplane.example.com", SecretKey: base64.StdEncoding.EncodeToString(make([]byte, 32)), Identity: config.IdentityConfig{AppID: "app"}, Notifications: config.NotificationsConfig{SMTPHost: "127.0.0.1", SMTPPort: 1, From: "loomplane@example.com"}}
	svc, err := New(cfg, db)
	if err != nil {
		t.Fatal(err)
	}
	if err = svc.mailStore.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err = svc.relayStore.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err = svc.Prepare(ctx); err != nil {
		t.Fatal(err)
	}
	svc.SetAuthorizer(&allowedProjects{})
	for _, status := range []string{"succeeded", "failed"} {
		if _, err = svc.relay.RegisterEventType(ctx, catalog.WebhookDefinition{Name: "deployment." + status, Version: "2026-10-18"}); err != nil {
			t.Fatal(err)
		}
	}
	insert := func(id string) {
		t.Helper()
		if _, insertErr := svc.pg.Exec(ctx, `INSERT INTO loomplane_deployments(id,org_id,project_id,environment_id,plan_id,started_by,status)VALUES($1,'org-a','project-a','env-a','plan-a','user-a','succeeded')`, id); insertErr != nil {
			t.Fatal(insertErr)
		}
	}
	insert("deployment-retry")
	if err = svc.drain(ctx); err != nil {
		t.Fatal(err)
	}
	var emailState, lastError string
	var complete bool
	if err = svc.pg.QueryRow(ctx, `SELECT email_state,last_error,completed_at IS NOT NULL FROM loomplane_notification_outbox WHERE deployment_id='deployment-retry'`).Scan(&emailState, &lastError, &complete); err != nil {
		t.Fatal(err)
	}
	if emailState != "pending" || complete || lastError != "email delivery failed" {
		t.Fatalf("failed SMTP was lost: state=%s complete=%t error=%s", emailState, complete, lastError)
	}
	if _, err = svc.pg.Exec(ctx, `UPDATE loomplane_notification_outbox SET attempts=9,next_attempt_at=clock_timestamp()`); err != nil {
		t.Fatal(err)
	}
	if err = svc.drain(ctx); err != nil {
		t.Fatal(err)
	}
	if err = svc.Health(ctx); err == nil {
		t.Fatal("exhausted mail retries did not affect health")
	}
	p := security.Principal{OrgID: "org-a", UserID: "user-a"}
	inbox, err := svc.List(ctx, p)
	if err != nil || len(inbox.Items) != 1 || inbox.Items[0].EmailStatus != "failed" {
		t.Fatalf("failed mail missing from inbox: %+v %v", inbox, err)
	}
	host, port, messages := smtpSink(t)
	svc.cfg.Notifications.SMTPHost = host
	svc.cfg.Notifications.SMTPPort = port
	if err = svc.Prepare(ctx); err != nil {
		t.Fatal(err)
	}
	if err = svc.RetryExhausted(ctx, p); err != nil {
		t.Fatal(err)
	}
	if err = svc.drain(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case body := <-messages:
		if !strings.Contains(body, "https://loomplane.example.com/deployments/plan-a") {
			t.Fatal("deployment email lacks trusted review link")
		}
	case <-time.After(time.Second):
		t.Fatal("manual recovery did not deliver email")
	}
	if err = svc.Health(ctx); err != nil {
		t.Fatalf("health did not recover: %v", err)
	}
	off := false
	if _, err = svc.SetPreferences(ctx, p, PreferenceInput{DeploymentEmail: &off}); err != nil {
		t.Fatal(err)
	}
	insert("deployment-opt-out")
	if err = svc.drain(ctx); err != nil {
		t.Fatal(err)
	}
	if err = svc.pg.QueryRow(ctx, `SELECT email_state FROM loomplane_notification_outbox WHERE deployment_id='deployment-opt-out'`).Scan(&emailState); err != nil || emailState != "skipped" {
		t.Fatalf("opt-out ignored: %s %v", emailState, err)
	}
	if _, err = svc.pg.Exec(ctx, `DELETE FROM authsome_members WHERE org_id='org-a'`); err != nil {
		t.Fatal(err)
	}
	insert("deployment-removed-member")
	if err = svc.drain(ctx); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = svc.pg.QueryRow(ctx, `SELECT count(*) FROM herald_inbox`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("removed member received new inbox entry: %d %v", count, err)
	}
	select {
	case <-messages:
		t.Fatal("opted-out or removed member received email")
	default:
	}
}

func domainNoticeService(t *testing.T) (*Service, <-chan string) {
	t.Helper()
	db := notificationDatabase(t)
	ctx := t.Context()
	host, port, messages := smtpSink(t)
	cfg := config.Config{PublicURL: "https://loomplane.example.com/", SecretKey: base64.StdEncoding.EncodeToString(make([]byte, 32)), Identity: config.IdentityConfig{AppID: "app"}, Notifications: config.NotificationsConfig{SMTPHost: host, SMTPPort: port, From: "loomplane@example.com"}}
	svc, err := New(cfg, db)
	if err != nil {
		t.Fatal(err)
	}
	if err = svc.mailStore.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err = svc.relayStore.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err = svc.Prepare(ctx); err != nil {
		t.Fatal(err)
	}
	if err = svc.registerEventTypes(ctx); err != nil {
		t.Fatal(err)
	}
	svc.SetAuthorizer(cloudManagers{"user-a": true})
	for _, q := range []string{
		`INSERT INTO authsome_users(id,app_id,email)VALUES('user-c','app','user-c@example.com')`,
		`INSERT INTO authsome_members(org_id,user_id)VALUES('org-a','user-c')`,
	} {
		if _, err = svc.pg.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	return svc, messages
}

func TestRenewalOutboxDeliversToCloudManagersOnly(t *testing.T) {
	svc, messages := domainNoticeService(t)
	ctx := t.Context()
	manager := security.Principal{OrgID: "org-a", UserID: "user-a"}
	// The renewal email is about the user's money, so it ignores the
	// deployment email preference.
	off := false
	if _, err := svc.SetPreferences(ctx, manager, PreferenceInput{DeploymentEmail: &off}); err != nil {
		t.Fatal(err)
	}
	notice := RenewalNotice{OrgID: "org-a", RegistrationID: "reg_1", Domain: "acme.dev", RenewsOn: time.Date(2026, 11, 30, 0, 0, 0, 0, time.UTC), Currency: "USD", RenewalCost: "10.11"}
	for range 2 {
		if err := svc.EnqueueRenewalNotice(ctx, notice); err != nil {
			t.Fatal(err)
		}
	}
	ids := []string{}
	rows, err := svc.pg.Query(ctx, `SELECT id FROM loomplane_notification_outbox WHERE kind='domain.renewal' AND project_id='' AND attempt=0 AND id=deployment_id ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	rows.Close()
	if len(ids) != 2 || ids[0] != "renewal:reg_1:2026-11-30:user-a" || ids[1] != "renewal:reg_1:2026-11-30:user-c" {
		t.Fatalf("outbox rows: %v", ids)
	}
	if err = svc.drain(ctx); err != nil {
		t.Fatal(err)
	}
	list, err := svc.List(ctx, manager)
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 1 {
		t.Fatalf("inbox: %+v", list)
	}
	item := list.Items[0]
	if item.ActionURL != "https://loomplane.example.com/domains?registration=reg_1&action=auto-renew-off" || item.Title != "acme.dev renews on Nov 30, 2026" ||
		item.Body != "Cloudflare renews acme.dev on Nov 30, 2026 for USD 10.11. Turn auto-renew off before then if you don't want to keep it." || item.EmailStatus != "sent" {
		t.Fatalf("inbox item: %+v", item)
	}
	if err = svc.MarkRead(ctx, manager, item.ID); err != nil {
		t.Fatal("mark read", err)
	}
	select {
	case body := <-messages:
		if !strings.Contains(body, "Subject: Loomplane: acme.dev renews on Nov 30, 2026") || !strings.Contains(body, "user-a@example.com") {
			t.Fatalf("renewal email: %s", body)
		}
	case <-time.After(time.Second):
		t.Fatal("renewal email was not delivered")
	}
	var inboxDone, complete bool
	var emailState string
	if err = svc.pg.QueryRow(ctx, `SELECT inbox_done,email_state,completed_at IS NOT NULL FROM loomplane_notification_outbox WHERE user_id='user-c'`).Scan(&inboxDone, &emailState, &complete); err != nil {
		t.Fatal(err)
	}
	if !inboxDone || emailState != "skipped" || !complete {
		t.Fatalf("member without cloud management: inbox_done=%t email=%s complete=%t", inboxDone, emailState, complete)
	}
	other, err := svc.List(ctx, security.Principal{OrgID: "org-a", UserID: "user-c"})
	if err != nil || len(other.Items) != 0 {
		t.Fatalf("member without cloud management sees: %+v %v", other, err)
	}
	var count int
	if err = svc.pg.QueryRow(ctx, `SELECT count(*) FROM herald_inbox`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("inbox rows: %d %v", count, err)
	}
	// One webhook per notice, however many members the organization has.
	types := []string{}
	rows, err = svc.pg.Query(ctx, `SELECT type FROM relay_events`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var eventType string
		if err = rows.Scan(&eventType); err != nil {
			t.Fatal(err)
		}
		types = append(types, eventType)
	}
	rows.Close()
	if len(types) != 1 || types[0] != "domain.renewal_upcoming" {
		t.Fatalf("webhook events: %v", types)
	}
	select {
	case <-messages:
		t.Fatal("a member without cloud management received email")
	default:
	}
}

func TestRegistrationResultOutboxNotifiesCloudManagers(t *testing.T) {
	svc, messages := domainNoticeService(t)
	ctx := t.Context()
	for range 2 {
		if err := svc.EnqueueRegistrationResult(ctx, RegistrationResultNotice{OrgID: "org-a", RegistrationID: "reg_1", Domain: "acme.dev", Status: "succeeded"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := svc.drain(ctx); err != nil {
		t.Fatal(err)
	}
	list, err := svc.List(ctx, security.Principal{OrgID: "org-a", UserID: "user-a"})
	if err != nil || len(list.Items) != 1 {
		t.Fatalf("inbox: %+v %v", list, err)
	}
	if item := list.Items[0]; item.Title != "acme.dev is registered" || item.ActionURL != "https://loomplane.example.com/domains?registration=reg_1" || item.Body != "acme.dev is registered in your Cloudflare account, and Loomplane manages its DNS." {
		t.Fatalf("inbox item: %+v", item)
	}
	var eventType string
	if err = svc.pg.QueryRow(ctx, `SELECT type FROM relay_events`).Scan(&eventType); err != nil || eventType != "domain.registration_succeeded" {
		t.Fatalf("webhook: %s %v", eventType, err)
	}
	select {
	case body := <-messages:
		if !strings.Contains(body, "Subject: Loomplane: acme.dev is registered") {
			t.Fatalf("registration email: %s", body)
		}
	case <-time.After(time.Second):
		t.Fatal("registration email was not delivered")
	}
}

// aiAdmins grants ai:manage to the listed users only, and everything else
// to everyone. It can take the grant away again.
type aiAdmins struct {
	mu    sync.Mutex
	users map[string]bool
}

func (a *aiAdmins) Require(_ context.Context, p security.Principal, permission security.Permission, _ string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if permission == security.AIManage && !a.users[p.UserID] {
		return security.ErrForbidden
	}
	return nil
}

func (a *aiAdmins) revoke(user string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.users, user)
}

// TestModelBudgetNoticesAreForAdmins: Rulings M8 and M24. A budget notice
// queued inside its caller's transaction goes to admins, who hold
// ai:manage, in the inbox and by email. A developer gets no inbox row
// and no email, and the organization gets one webhook. An admin who loses
// ai:manage no longer sees it, and a transaction that rolls back queues
// nothing.
func TestModelBudgetNoticesAreForAdmins(t *testing.T) {
	svc, messages := domainNoticeService(t)
	ctx := t.Context()
	authz := &aiAdmins{users: map[string]bool{"user-a": true}}
	svc.SetAuthorizer(authz)
	notice := ModelBudgetNotice{
		OrgID: "org-a", ProjectID: "project-a", EnvironmentID: "env-prod", DeploymentID: "aimodel_1", Resource: "llama",
		Month: "2026-11", Kind: ModelBudgetProjected, ProjectedUSD: "676.80", BudgetUSD: "500.00",
	}
	enqueue := func(commit bool) {
		tx, err := svc.pg.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err = svc.EnqueueModelBudgetNoticeIn(ctx, tx, notice); err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
		if !commit {
			if err = tx.Rollback(); err != nil {
				t.Fatal(err)
			}
			return
		}
		if err = tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	admin, developer := security.Principal{OrgID: "org-a", UserID: "user-a"}, security.Principal{OrgID: "org-a", UserID: "user-c"}
	// An endpoint made now hears the budget events.
	hook, err := svc.CreateEndpoint(ctx, admin, WebhookEndpointInput{URL: "https://8.8.8.8/hook", Description: "Budgets"})
	if err != nil {
		t.Fatal(err)
	}
	enqueue(false)
	var count int
	if err = svc.pg.QueryRow(ctx, `SELECT count(*) FROM loomplane_notification_outbox`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("a rolled back notice queued %d rows: %v", count, err)
	}
	enqueue(true)
	enqueue(true) // the same notice again adds nothing
	if err = svc.pg.QueryRow(ctx, `SELECT count(*) FROM loomplane_notification_outbox WHERE kind='model.budget' AND project_id='project-a'`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("outbox rows: %d %v", count, err)
	}
	if err = svc.drain(ctx); err != nil {
		t.Fatal(err)
	}

	list, err := svc.List(ctx, admin)
	if err != nil || len(list.Items) != 1 {
		t.Fatalf("admin's inbox: %+v %v", list, err)
	}
	item := list.Items[0]
	if item.Title != "llama is on track to cost $676.80 this month" ||
		item.Body != "Its budget is $500.00. Loomplane won't stop it. Open the model to change where it runs or remove it." ||
		item.ActionURL != "https://loomplane.example.com/projects/project-a/ai/models/llama?environment=env-prod" || item.ProjectID != "project-a" ||
		item.EmailStatus != "sent" {
		t.Fatalf("inbox item: %+v", item)
	}
	for _, text := range []string{item.Title, item.Body} {
		if strings.ContainsAny(text, "\u2014\u2013") {
			t.Errorf("a dash in %q", text)
		}
	}
	select {
	case body := <-messages:
		if !strings.Contains(body, "Subject: Loomplane: llama is on track to cost $676.80 this month") || !strings.Contains(body, "user-a@example.com") {
			t.Fatalf("budget email: %s", body)
		}
	case <-time.After(time.Second):
		t.Fatal("the admin's budget email was not delivered")
	}

	// The developer gets no inbox row and no email.
	var inboxDone, complete bool
	var emailState string
	if err = svc.pg.QueryRow(ctx, `SELECT inbox_done,email_state,completed_at IS NOT NULL FROM loomplane_notification_outbox WHERE user_id='user-c'`).Scan(&inboxDone, &emailState, &complete); err != nil {
		t.Fatal(err)
	}
	if !inboxDone || emailState != "skipped" || !complete {
		t.Fatalf("developer: inbox_done=%t email=%s complete=%t", inboxDone, emailState, complete)
	}
	if other, lerr := svc.List(ctx, developer); lerr != nil || len(other.Items) != 0 {
		t.Fatalf("the developer sees: %+v %v", other, lerr)
	}
	if err = svc.pg.QueryRow(ctx, `SELECT count(*) FROM herald_inbox`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("inbox rows: %d %v", count, err)
	}
	select {
	case <-messages:
		t.Fatal("the developer received budget email")
	default:
	}

	// One webhook for the organization, of the model group's type, and the
	// endpoint has it to deliver.
	var eventType string
	if err = svc.pg.QueryRow(ctx, `SELECT type FROM relay_events`).Scan(&eventType); err != nil || eventType != "model.budget_projected" {
		t.Fatalf("webhook: %s %v", eventType, err)
	}
	deliveries, err := svc.Deliveries(ctx, admin, hook.Endpoint.ID)
	if err != nil || len(deliveries.Items) != 1 || deliveries.Items[0].EventType != "model.budget_projected" {
		t.Fatalf("endpoint deliveries: %+v %v", deliveries, err)
	}

	// Ruling M32: the same notice for a new budget is a new notice; for
	// the same budget it queues nothing.
	notice.BudgetUSD = "1000.00"
	enqueue(true)
	enqueue(true)
	if err = svc.pg.QueryRow(ctx, `SELECT count(*) FROM loomplane_notification_outbox WHERE kind='model.budget'`).Scan(&count); err != nil || count != 4 {
		t.Fatalf("outbox rows after a new budget: %d %v", count, err)
	}
	var keys []string
	rows, err := svc.pg.Query(ctx, `SELECT id FROM loomplane_notification_outbox WHERE user_id='user-a' ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var key string
		if err = rows.Scan(&key); err != nil {
			t.Fatal(err)
		}
		keys = append(keys, key)
	}
	rows.Close()
	if len(keys) != 2 || keys[0] != "model-budget:aimodel_1:2026-11:projected:1000.00:user-a" || keys[1] != "model-budget:aimodel_1:2026-11:projected:500.00:user-a" {
		t.Fatalf("outbox keys %v", keys)
	}
	if err = svc.drain(ctx); err != nil {
		t.Fatal(err)
	}
	if list, err = svc.List(ctx, admin); err != nil || len(list.Items) != 2 {
		t.Fatalf("admin's inbox after a new budget: %+v %v", list, err)
	}
	select {
	case <-messages:
	case <-time.After(time.Second):
		t.Fatal("the new budget's email was not delivered")
	}
	item = list.Items[0]
	if item.Title != "llama is on track to cost $676.80 this month" || !strings.Contains(item.Body, "$1000.00") {
		t.Errorf("new budget's item: %+v", item)
	}

	// An admin who loses ai:manage no longer sees it.
	authz.revoke("user-a")
	if list, err = svc.List(ctx, admin); err != nil || len(list.Items) != 0 {
		t.Errorf("former admin's inbox: %+v %v", list, err)
	}
	if err = svc.MarkRead(ctx, admin, item.ID); !errors.Is(err, security.ErrForbidden) {
		t.Errorf("former admin marks it read: %v", err)
	}
}
