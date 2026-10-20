package notifications

import (
	"context"
	"errors"
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
