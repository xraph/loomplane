package notifications

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/xraph/herald"
	"github.com/xraph/herald/device"

	"github.com/xraph/loomplane/internal/security"
)

// ErrPushFailed means the push gateway didn't accept a message.
var ErrPushFailed = errors.New("push delivery failed")

// ErrTooSoon means a test push was asked for again before the wait was over.
var ErrTooSoon = errors.New("wait a moment before sending another test")

const (
	maxDevicesPerUser = 20
	maxHandleBytes    = 4096
	maxDeviceName     = 100
	publicKeyBytes    = 32
)

type QuietHours struct {
	Start string `json:"start"`
	End   string `json:"end"`
}

// PushDevice is a phone the signed-in person receives pushes on.
type PushDevice struct {
	ID         string            `json:"id"`
	Platform   string            `json:"platform"`
	Name       string            `json:"name"`
	TimeZone   string            `json:"time_zone"`
	QuietHours *QuietHours       `json:"quiet_hours"`
	Settings   map[string]string `json:"settings"`
	CreatedAt  time.Time         `json:"created_at"`
	LastSeenAt time.Time         `json:"last_seen_at"`
}

type RegisterDeviceInput struct {
	Handle    string            `json:"handle"`
	PublicKey string            `json:"public_key"`
	Platform  string            `json:"platform"`
	Name      string            `json:"name"`
	TimeZone  string            `json:"time_zone"`
	Settings  map[string]string `json:"settings"`
}

type UpdateDeviceInput struct {
	TimeZone   *string            `json:"time_zone,omitempty"`
	Settings   map[string]string  `json:"settings,omitempty"` // merged over the stored settings
	QuietHours OptionalQuietHours `json:"quiet_hours"`        // absent keeps, null clears, object sets
}

// OptionalQuietHours tells an absent field from null and from a value.
type OptionalQuietHours struct {
	Set   bool
	Value *QuietHours
}

// UnmarshalJSON runs only when the field is present, so it records Set.
func (o *OptionalQuietHours) UnmarshalJSON(b []byte) error {
	o.Set = true
	if bytes.Equal(bytes.TrimSpace(b), []byte("null")) {
		o.Value = nil
		return nil
	}
	o.Value = &QuietHours{}
	return json.Unmarshal(b, o.Value)
}

// DefaultSettings are the per-device switches a new phone starts with.
var DefaultSettings = map[string]string{
	"level.production": "everything", "level.staging": "critical",
	"push.approvals": "true", "push.alerts": "true", "push.failed": "true", "push.rollbacks": "true",
	"push.live": "false", "push.progress": "true", "push.workspace": "true",
}

var DefaultQuietHours = QuietHours{Start: "22:00", End: "07:00"}

func validSetting(key, value string) bool {
	if strings.HasPrefix(key, "level.") {
		return value == "everything" || value == "critical" || value == "off"
	}
	return value == "true" || value == "false"
}

// normalizeSettings lays defaults, then stored values, then in. Unknown keys
// are dropped and a bad value in in is ErrInvalid. A bad stored value falls
// back to its default.
func normalizeSettings(stored, in map[string]string) (map[string]string, error) {
	out := make(map[string]string, len(DefaultSettings))
	for key, def := range DefaultSettings {
		out[key] = def
		if v, ok := stored[key]; ok && validSetting(key, v) {
			out[key] = v
		}
		if v, ok := in[key]; ok {
			if !validSetting(key, v) {
				return nil, fmt.Errorf("%w: %q is not a valid value for %s", ErrInvalid, v, key)
			}
			out[key] = v
		}
	}
	return out, nil
}

func validateQuietHours(q QuietHours) error {
	if q.Start == q.End {
		return fmt.Errorf("%w: quiet hours start and end must differ", ErrInvalid)
	}
	if err := (device.QuietHours{Start: q.Start, End: q.End}).Validate(); err != nil {
		return fmt.Errorf("%w: quiet hours are HH:MM on a 24-hour clock", ErrInvalid)
	}
	return nil
}

func validateTimeZone(zone string) error {
	if zone == "" || zone == "Local" {
		return fmt.Errorf("%w: time_zone must be an IANA name such as Europe/London", ErrInvalid)
	}
	if _, err := time.LoadLocation(zone); err != nil {
		return fmt.Errorf("%w: unknown time zone %q", ErrInvalid, zone)
	}
	return nil
}

type validDevice struct {
	RegisterDeviceInput
	key []byte
}

func validateRegister(in RegisterDeviceInput) (validDevice, error) {
	if !strings.HasPrefix(in.Handle, "h1.") || len(in.Handle) > maxHandleBytes {
		return validDevice{}, fmt.Errorf("%w: the push handle is not valid", ErrInvalid)
	}
	key, err := base64.StdEncoding.DecodeString(in.PublicKey)
	if err != nil || len(key) != publicKeyBytes {
		return validDevice{}, fmt.Errorf("%w: public_key is the base64 of a 32-byte key", ErrInvalid)
	}
	if in.Platform != "android" && in.Platform != "ios" {
		return validDevice{}, fmt.Errorf("%w: platform is android or ios", ErrInvalid)
	}
	if utf8.RuneCountInString(in.Name) > maxDeviceName || strings.IndexFunc(in.Name, unicode.IsControl) >= 0 {
		return validDevice{}, fmt.Errorf("%w: a device name is at most %d characters with no control characters", ErrInvalid, maxDeviceName)
	}
	if in.Name == "" {
		in.Name = "Phone"
	}
	if zoneErr := validateTimeZone(in.TimeZone); zoneErr != nil {
		return validDevice{}, zoneErr
	}
	if _, err = normalizeSettings(nil, in.Settings); err != nil {
		return validDevice{}, err
	}
	return validDevice{RegisterDeviceInput: in, key: key}, nil
}

func (s *Service) deviceWriter(p security.Principal) error {
	if !s.PushEnabled() {
		return ErrPushDisabled
	}
	if p.UserID == "" {
		return security.ErrUnauthenticated
	}
	if p.ReadOnly || p.IsServiceAccount() {
		return fmt.Errorf("%w: only a signed-in person can manage their phones", security.ErrForbidden)
	}
	return nil
}

func pushDevice(d *device.Device) PushDevice {
	out := PushDevice{ID: d.ID, Platform: d.Platform, Name: d.Name, TimeZone: d.TimeZone, Settings: d.Settings, CreatedAt: d.CreatedAt, LastSeenAt: d.LastSeenAt}
	if d.QuietHours != nil {
		out.QuietHours = &QuietHours{Start: d.QuietHours.Start, End: d.QuietHours.End}
	}
	return out
}

func deviceError(err error) error {
	switch {
	case errors.Is(err, herald.ErrDeviceNotFound):
		return ErrNotFound
	case errors.Is(err, device.ErrInvalidDevice):
		return fmt.Errorf("%w: %s", ErrInvalid, err.Error())
	}
	return err
}

// RegisterDevice stores or refreshes the caller's phone. The same public key
// is the same phone, so it keeps its quiet hours and settings.
func (s *Service) RegisterDevice(ctx context.Context, p security.Principal, in RegisterDeviceInput) (PushDevice, error) {
	if err := s.deviceWriter(p); err != nil {
		return PushDevice{}, err
	}
	v, err := validateRegister(in)
	if err != nil {
		return PushDevice{}, err
	}
	appID := s.cfg.Identity.AppID
	existing, err := s.herald.ListDevices(ctx, appID, p.UserID)
	if err != nil {
		return PushDevice{}, err
	}
	d := &device.Device{AppID: appID, UserID: p.UserID, Channel: "push", Provider: "sealed", Address: v.Handle, PublicKey: v.key,
		Platform: v.Platform, Name: v.Name, TimeZone: v.TimeZone, QuietHours: &device.QuietHours{Start: DefaultQuietHours.Start, End: DefaultQuietHours.End}, LastSeenAt: time.Now().UTC()}
	var stored map[string]string
	for _, old := range existing {
		if bytes.Equal(old.PublicKey, v.key) {
			d.QuietHours, stored = old.QuietHours, old.Settings
			break
		}
	}
	if d.Settings, err = normalizeSettings(stored, in.Settings); err != nil {
		return PushDevice{}, err
	}
	if err = s.herald.SaveDevice(ctx, d); err != nil {
		return PushDevice{}, deviceError(err)
	}
	s.dropStalest(ctx, appID, p.UserID, d.ID)
	return pushDevice(d), nil
}

// dropStalest keeps a person's phones to the most recently seen twenty. It
// never removes keep, and a failure here doesn't undo the registration.
func (s *Service) dropStalest(ctx context.Context, appID, userID, keep string) {
	list, err := s.herald.ListDevices(ctx, appID, userID)
	if err != nil || len(list) <= maxDevicesPerUser {
		return
	}
	slices.SortStableFunc(list, func(a, b *device.Device) int { return a.LastSeenAt.Compare(b.LastSeenAt) })
	for _, old := range list[:len(list)-maxDevicesPerUser] {
		if old.ID != keep {
			if err := s.herald.DeleteDevice(ctx, appID, old.ID); err != nil {
				slog.WarnContext(ctx, "drop stale push device", "device", old.ID, "error", err)
			}
		}
	}
}

// ownedDevice answers ErrNotFound for a device that isn't the caller's.
func (s *Service) ownedDevice(ctx context.Context, p security.Principal, id string) (*device.Device, error) {
	d, err := s.herald.GetDevice(ctx, s.cfg.Identity.AppID, id)
	if err != nil {
		return nil, deviceError(err)
	}
	if d.AppID != s.cfg.Identity.AppID || d.UserID != p.UserID {
		return nil, ErrNotFound
	}
	return d, nil
}

func (s *Service) UpdateDevice(ctx context.Context, p security.Principal, id string, in UpdateDeviceInput) (PushDevice, error) {
	if err := s.deviceWriter(p); err != nil {
		return PushDevice{}, err
	}
	d, err := s.ownedDevice(ctx, p, id)
	if err != nil {
		return PushDevice{}, err
	}
	if in.TimeZone != nil {
		if zoneErr := validateTimeZone(*in.TimeZone); zoneErr != nil {
			return PushDevice{}, zoneErr
		}
		d.TimeZone = *in.TimeZone
	}
	if d.Settings, err = normalizeSettings(d.Settings, in.Settings); err != nil {
		return PushDevice{}, err
	}
	if in.QuietHours.Set {
		if in.QuietHours.Value == nil {
			d.QuietHours = nil
		} else {
			if quietErr := validateQuietHours(*in.QuietHours.Value); quietErr != nil {
				return PushDevice{}, quietErr
			}
			d.QuietHours = &device.QuietHours{Start: in.QuietHours.Value.Start, End: in.QuietHours.Value.End}
		}
	}
	d.LastSeenAt = time.Now().UTC()
	if err = s.herald.SaveDevice(ctx, d); err != nil {
		return PushDevice{}, deviceError(err)
	}
	return pushDevice(d), nil
}

func (s *Service) DeleteDevice(ctx context.Context, p security.Principal, id string) error {
	if err := s.deviceWriter(p); err != nil {
		return err
	}
	d, err := s.ownedDevice(ctx, p, id)
	if err != nil {
		return err
	}
	return deviceError(s.herald.DeleteDevice(ctx, d.AppID, d.ID))
}

// DeleteUserDevices drops every phone of a user. It doesn't depend on the push
// setting: a deleted account loses its phones either way.
func (s *Service) DeleteUserDevices(ctx context.Context, userID string) error {
	return s.herald.DeleteUserDevices(ctx, s.cfg.Identity.AppID, userID)
}
