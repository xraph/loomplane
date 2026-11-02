package notifications

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/xraph/authsome/bridge"
	"github.com/xraph/herald"
	"github.com/xraph/herald/driver"
	"github.com/xraph/herald/message"
	"github.com/xraph/herald/provider"
	heraldmem "github.com/xraph/herald/store/memory"

	"github.com/xraph/loomplane/internal/config"
)

type rejectingDriver struct{}

func (rejectingDriver) Name() string                                        { return "test" }
func (rejectingDriver) Channel() string                                     { return "email" }
func (rejectingDriver) Validate(map[string]string, map[string]string) error { return nil }
func (rejectingDriver) Send(context.Context, *driver.OutboundMessage) (*driver.DeliveryResult, error) {
	return nil, errors.New("token=private provider rejection")
}

func TestAuthBridgeReportsFailedHeraldResultWithoutLeakingProviderDetails(t *testing.T) {
	ctx := context.Background()
	store := heraldmem.New()
	engine, err := herald.New(herald.WithStore(privateHeraldStore{Store: store}), herald.WithDriver(rejectingDriver{}))
	if err != nil {
		t.Fatal(err)
	}
	if err = engine.SeedDefaultTemplates(ctx, "app"); err != nil {
		t.Fatal(err)
	}
	if err = engine.CreateProvider(ctx, &provider.Provider{AppID: "app", Name: "test", Channel: "email", Driver: "test", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	svc := &Service{cfg: config.Config{Identity: config.IdentityConfig{AppID: "app"}, Notifications: config.NotificationsConfig{SMTPHost: "localhost"}}, herald: engine}
	req := &bridge.HeraldSendRequest{Channel: "email", Template: "auth.password-reset", To: []string{"test@example.com"}, Data: map[string]any{"user_name": "Test", "app_name": "Loomplane", "reset_url": "https://app.example/reset?token=private"}}
	if err = svc.AuthBridge().Send(ctx, req); !errors.Is(err, ErrDeliveryFailed) {
		t.Fatalf("failed result reported as success: %v", err)
	}
	logs, err := store.ListMessages(ctx, "app", message.ListOptions{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(logs) != 1 || logs[0].Error != "delivery failed" || logs[0].Recipient != "" {
		t.Fatalf("unsafe diagnostics: %#v", logs)
	}
}
func TestAuthBridgeDisabledMailDoesNotPretendDelivery(t *testing.T) {
	svc := &Service{}
	if err := svc.AuthBridge().Send(context.Background(), &bridge.HeraldSendRequest{Channel: "email"}); !errors.Is(err, ErrMailDisabled) {
		t.Fatalf("disabled delivery: %v", err)
	}
}

type capturingDriver struct{ got []*driver.OutboundMessage }

func (*capturingDriver) Name() string                                        { return "capture" }
func (*capturingDriver) Channel() string                                     { return "email" }
func (*capturingDriver) Validate(map[string]string, map[string]string) error { return nil }
func (c *capturingDriver) Send(_ context.Context, m *driver.OutboundMessage) (*driver.DeliveryResult, error) {
	c.got = append(c.got, m)
	return &driver.DeliveryResult{Status: message.StatusSent}, nil
}

func TestSendWaitlistApproved(t *testing.T) {
	ctx := context.Background()
	store := heraldmem.New()
	sink := &capturingDriver{}
	engine, err := herald.New(herald.WithStore(privateHeraldStore{Store: store}), herald.WithDriver(sink))
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{PublicURL: "https://loom.example", Identity: config.IdentityConfig{AppID: "app"}, Notifications: config.NotificationsConfig{SMTPHost: "localhost"}}
	svc := &Service{cfg: cfg, herald: engine, mailStore: privateHeraldStore{Store: store}}
	// Seeding twice must not duplicate the template.
	for range 2 {
		if err = svc.seedTemplates(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if err = engine.CreateProvider(ctx, &provider.Provider{AppID: "app", Name: "test", Channel: "email", Driver: "capture", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if err = svc.SendWaitlistApproved(ctx, "ada+x@example.com"); err != nil {
		t.Fatal(err)
	}
	if len(sink.got) != 1 {
		t.Fatalf("sent %d messages", len(sink.got))
	}
	m := sink.got[0]
	const link = "https://loom.example/sign-up?email=ada%2Bx%40example.com"
	if m.Subject != "You're in: create your Loomplane account" || m.Text != "Your spot on the Loomplane waitlist was approved. Create your account here: "+link {
		t.Fatalf("unexpected message: %q / %q", m.Subject, m.Text)
	}
	if !strings.Contains(m.HTML, `href="`+link+`"`) {
		t.Fatalf("html lacks the link: %s", m.HTML)
	}
}

func TestSendWaitlistApprovedMailDisabled(t *testing.T) {
	svc := &Service{cfg: config.Config{PublicURL: "https://loom.example"}}
	if err := svc.SendWaitlistApproved(context.Background(), "ada@example.com"); !errors.Is(err, ErrMailDisabled) {
		t.Fatalf("error = %v", err)
	}
}
