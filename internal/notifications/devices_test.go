package notifications

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"maps"
	"strings"
	"testing"
)

func TestNormalizeSettingsFillsDefaults(t *testing.T) {
	got, err := normalizeSettings(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !maps.Equal(got, DefaultSettings) {
		t.Fatalf("settings = %v", got)
	}
}

func TestNormalizeSettingsMergesAndDropsUnknown(t *testing.T) {
	got, err := normalizeSettings(map[string]string{"push.live": "true"}, map[string]string{"level.staging": "off", "future.key": "x"})
	if err != nil {
		t.Fatal(err)
	}
	if got["push.live"] != "true" || got["level.staging"] != "off" {
		t.Fatalf("settings = %v", got)
	}
	if _, ok := got["future.key"]; ok {
		t.Fatalf("unknown key kept: %v", got)
	}
}

func TestNormalizeSettingsRejectsBadValues(t *testing.T) {
	_, err := normalizeSettings(nil, map[string]string{"level.production": "loud"})
	if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "level.production") {
		t.Fatalf("err = %v", err)
	}
	_, err = normalizeSettings(nil, map[string]string{"push.live": "yes"})
	if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "push.live") {
		t.Fatalf("err = %v", err)
	}
}

func TestOptionalQuietHoursDecodes(t *testing.T) {
	var in UpdateDeviceInput
	if err := json.Unmarshal([]byte(`{}`), &in); err != nil || in.QuietHours.Set {
		t.Fatalf("absent: %+v %v", in.QuietHours, err)
	}
	in = UpdateDeviceInput{}
	if err := json.Unmarshal([]byte(`{"quiet_hours":null}`), &in); err != nil || !in.QuietHours.Set || in.QuietHours.Value != nil {
		t.Fatalf("null: %+v %v", in.QuietHours, err)
	}
	in = UpdateDeviceInput{}
	if err := json.Unmarshal([]byte(`{"quiet_hours":{"start":"23:00","end":"06:30"}}`), &in); err != nil || !in.QuietHours.Set || in.QuietHours.Value == nil || in.QuietHours.Value.Start != "23:00" {
		t.Fatalf("object: %+v %v", in.QuietHours, err)
	}
}

func validRegister() RegisterDeviceInput {
	return RegisterDeviceInput{Handle: "h1.abc", PublicKey: base64.StdEncoding.EncodeToString(make([]byte, 32)), Platform: "ios", Name: "Rex's phone", TimeZone: "Europe/London"}
}

func TestRegisterInputValidation(t *testing.T) {
	if _, err := validateRegister(validRegister()); err != nil {
		t.Fatalf("valid input: %v", err)
	}
	zone := validRegister()
	zone.TimeZone = "America/New_York"
	if _, err := validateRegister(zone); err != nil {
		t.Fatalf("America/New_York: %v", err)
	}
	cases := map[string]func(*RegisterDeviceInput){
		"handle":       func(in *RegisterDeviceInput) { in.Handle = "x" },
		"long handle":  func(in *RegisterDeviceInput) { in.Handle = "h1." + strings.Repeat("a", 4100) },
		"short key":    func(in *RegisterDeviceInput) { in.PublicKey = base64.StdEncoding.EncodeToString(make([]byte, 31)) },
		"key encoding": func(in *RegisterDeviceInput) { in.PublicKey = "!!" },
		"platform":     func(in *RegisterDeviceInput) { in.Platform = "web" },
		"zone":         func(in *RegisterDeviceInput) { in.TimeZone = "Mars/Base" },
		"local zone":   func(in *RegisterDeviceInput) { in.TimeZone = "Local" },
		"empty zone":   func(in *RegisterDeviceInput) { in.TimeZone = "" },
		"name":         func(in *RegisterDeviceInput) { in.Name = "a\nb" },
		"long name":    func(in *RegisterDeviceInput) { in.Name = strings.Repeat("é", 101) },
		"setting":      func(in *RegisterDeviceInput) { in.Settings = map[string]string{"push.live": "maybe"} },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			in := validRegister()
			mutate(&in)
			if _, err := validateRegister(in); !errors.Is(err, ErrInvalid) {
				t.Fatalf("err = %v", err)
			}
		})
	}
	empty := validRegister()
	empty.Name = ""
	if got, err := validateRegister(empty); err != nil || got.Name != "Phone" {
		t.Fatalf("empty name: %+v %v", got, err)
	}
}

func TestQuietHoursValidation(t *testing.T) {
	for _, q := range []QuietHours{{"22:00", "22:00"}, {"24:00", "07:00"}, {"7:00", "08:00"}, {"", ""}} {
		if err := validateQuietHours(q); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%+v: %v", q, err)
		}
	}
	if err := validateQuietHours(QuietHours{"23:00", "06:30"}); err != nil {
		t.Fatal(err)
	}
}
