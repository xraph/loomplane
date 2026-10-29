//go:build integration

package notifications

import (
	"encoding/base64"
	"errors"
	"fmt"
	"maps"
	"testing"

	authid "github.com/xraph/authsome/id"

	"github.com/xraph/loomplane/internal/security"
)

func deviceKey(n int) string {
	b := make([]byte, 32)
	b[0], b[1] = byte(n), byte(n>>8)
	b[31] = 1
	return base64.StdEncoding.EncodeToString(b)
}

func deviceInput(n int) RegisterDeviceInput {
	return RegisterDeviceInput{Handle: fmt.Sprintf("h1.handle%d", n), PublicKey: deviceKey(n), Platform: "ios", Name: "Phone", TimeZone: "Europe/London"}
}

func person(id string) security.Principal { return security.Principal{UserID: id, OrgID: "org"} }

func TestRegisterDeviceStoresDefaults(t *testing.T) {
	svc := pushService(t, notificationDatabase(t), make([]byte, 32), true)
	got, err := svc.RegisterDevice(t.Context(), person("user-a"), deviceInput(1))
	if err != nil {
		t.Fatal(err)
	}
	if !maps.Equal(got.Settings, DefaultSettings) || got.QuietHours == nil || *got.QuietHours != DefaultQuietHours || got.Platform != "ios" || got.ID == "" {
		t.Fatalf("device = %+v", got)
	}
}

func TestRegisterSamePublicKeyReplacesHandle(t *testing.T) {
	svc := pushService(t, notificationDatabase(t), make([]byte, 32), true)
	first, err := svc.RegisterDevice(t.Context(), person("user-a"), deviceInput(1))
	if err != nil {
		t.Fatal(err)
	}
	in := deviceInput(1)
	in.Handle = "h1.fresh"
	second, err := svc.RegisterDevice(t.Context(), person("user-a"), in)
	if err != nil {
		t.Fatal(err)
	}
	list, err := svc.herald.ListDevices(t.Context(), "app", "user-a")
	if err != nil {
		t.Fatal(err)
	}
	if second.ID != first.ID || len(list) != 1 || list[0].Address != "h1.fresh" {
		t.Fatalf("second=%+v list=%d", second, len(list))
	}
}

func TestRegisterSamePublicKeyKeepsQuietHoursAndSettings(t *testing.T) {
	svc := pushService(t, notificationDatabase(t), make([]byte, 32), true)
	first, err := svc.RegisterDevice(t.Context(), person("user-a"), deviceInput(1))
	if err != nil {
		t.Fatal(err)
	}
	quiet := &QuietHours{Start: "23:30", End: "06:00"}
	if _, err = svc.UpdateDevice(t.Context(), person("user-a"), first.ID, UpdateDeviceInput{QuietHours: OptionalQuietHours{Set: true, Value: quiet}, Settings: map[string]string{"push.live": "true"}}); err != nil {
		t.Fatal(err)
	}
	again, err := svc.RegisterDevice(t.Context(), person("user-a"), deviceInput(1))
	if err != nil {
		t.Fatal(err)
	}
	if again.QuietHours == nil || *again.QuietHours != *quiet || again.Settings["push.live"] != "true" {
		t.Fatalf("re-register reset the device: %+v", again)
	}
	in := deviceInput(1)
	in.Settings = map[string]string{"push.alerts": "false"}
	again, err = svc.RegisterDevice(t.Context(), person("user-a"), in)
	if err != nil {
		t.Fatal(err)
	}
	if again.Settings["push.alerts"] != "false" || again.Settings["push.live"] != "true" {
		t.Fatalf("settings = %v", again.Settings)
	}
}

func TestRegisterBeyondTwentyDropsTheStalest(t *testing.T) {
	svc := pushService(t, notificationDatabase(t), make([]byte, 32), true)
	var firstID string
	for i := 1; i <= 21; i++ {
		got, err := svc.RegisterDevice(t.Context(), person("user-a"), deviceInput(i))
		if err != nil {
			t.Fatal(err)
		}
		if i == 1 {
			firstID = got.ID
		}
	}
	list, err := svc.herald.ListDevices(t.Context(), "app", "user-a")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != maxDevicesPerUser {
		t.Fatalf("devices = %d", len(list))
	}
	for _, d := range list {
		if d.ID == firstID {
			t.Fatal("the stalest device was kept")
		}
	}
}

func TestOtherUsersDeviceIsNotFound(t *testing.T) {
	svc := pushService(t, notificationDatabase(t), make([]byte, 32), true)
	got, err := svc.RegisterDevice(t.Context(), person("user-a"), deviceInput(1))
	if err != nil {
		t.Fatal(err)
	}
	zone := "Asia/Tokyo"
	if _, err = svc.UpdateDevice(t.Context(), person("user-b"), got.ID, UpdateDeviceInput{TimeZone: &zone}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("update err = %v", err)
	}
	if err = svc.DeleteDevice(t.Context(), person("user-b"), got.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete err = %v", err)
	}
	list, _ := svc.herald.ListDevices(t.Context(), "app", "user-a")
	if len(list) != 1 || list[0].TimeZone != "Europe/London" {
		t.Fatalf("device changed: %+v", list)
	}
}

func TestUpdateDeviceClearsQuietHours(t *testing.T) {
	svc := pushService(t, notificationDatabase(t), make([]byte, 32), true)
	got, err := svc.RegisterDevice(t.Context(), person("user-a"), deviceInput(1))
	if err != nil {
		t.Fatal(err)
	}
	out, err := svc.UpdateDevice(t.Context(), person("user-a"), got.ID, UpdateDeviceInput{QuietHours: OptionalQuietHours{Set: true}})
	if err != nil {
		t.Fatal(err)
	}
	if out.QuietHours != nil {
		t.Fatalf("quiet hours = %+v", out.QuietHours)
	}
	kept, err := svc.UpdateDevice(t.Context(), person("user-a"), got.ID, UpdateDeviceInput{Settings: map[string]string{"push.live": "true"}})
	if err != nil || kept.QuietHours != nil || kept.Settings["push.live"] != "true" {
		t.Fatalf("absent quiet_hours changed it: %+v %v", kept, err)
	}
}

func TestDeviceWritesRefuseReadOnlyAndAutomation(t *testing.T) {
	svc := pushService(t, notificationDatabase(t), make([]byte, 32), true)
	for _, p := range []security.Principal{{UserID: "user-a", ReadOnly: true}, security.SystemPrincipal("org")} {
		if _, err := svc.RegisterDevice(t.Context(), p, deviceInput(1)); !errors.Is(err, security.ErrForbidden) {
			t.Fatalf("%+v: %v", p, err)
		}
	}
}

func TestDeviceRoutesAre404WhenPushIsOff(t *testing.T) {
	svc := pushService(t, notificationDatabase(t), make([]byte, 32), true)
	svc.cfg.Push.Enabled = false
	if _, err := svc.RegisterDevice(t.Context(), person("user-a"), deviceInput(1)); !errors.Is(err, ErrPushDisabled) {
		t.Fatalf("err = %v", err)
	}
}

func TestDeletedAccountLosesItsDevices(t *testing.T) {
	svc := pushService(t, notificationDatabase(t), make([]byte, 32), true)
	a, b := authid.NewUserID(), authid.NewUserID()
	for i, user := range []authid.UserID{a, a, b} {
		if _, err := svc.RegisterDevice(t.Context(), person(user.String()), deviceInput(i+1)); err != nil {
			t.Fatal(err)
		}
	}
	if err := NewAccountCleanup(svc).OnAfterUserDelete(t.Context(), a); err != nil {
		t.Fatal(err)
	}
	left, _ := svc.herald.ListDevices(t.Context(), "app", a.String())
	kept, _ := svc.herald.ListDevices(t.Context(), "app", b.String())
	if len(left) != 0 || len(kept) != 1 {
		t.Fatalf("a=%d b=%d", len(left), len(kept))
	}
}
