package notifications

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/xraph/herald/device"
	"github.com/xraph/herald/driver"
)

func TestCollapseID(t *testing.T) {
	if got := collapseID("plan:plan_1"); got != "807b71c17bb57681" {
		t.Fatalf("collapse id: %s", got)
	}
}

func TestEnvelopeCutsOnRunes(t *testing.T) {
	got := cut(strings.Repeat("é", 100), 80)
	if !utf8.ValidString(got) || utf8.RuneCountInString(got) != 80 {
		t.Fatalf("cut: %d runes, valid %t", utf8.RuneCountInString(got), utf8.ValidString(got))
	}
	if short := cut("hi", 80); short != "hi" {
		t.Fatalf("short: %q", short)
	}
}

func TestEnvelopeDataKeys(t *testing.T) {
	p := &Push{Class: ClassNeedsYou, Urgency: driver.UrgencyCritical, Category: CategoryApproval, Actions: []string{ActionApprove, ActionReview}, Environment: "production", Data: map[string]string{"revision": "3"}}
	got := envelopeData(envelopeInput{Server: "https://lp.example.com", OrgID: "org-a", OrgName: "Acme", Kind: "plan.waiting", Subject: "plan_1", InboxID: "hinb_1", Path: "/plans/1", ProjectID: "project-a", Push: p,
		SentAt: time.Date(2026, 10, 28, 20, 44, 0, 0, time.UTC)})
	want := map[string]string{"server": "https://lp.example.com", "org": "org-a", "org_name": "Acme", "kind": "plan.waiting", "subject": "plan_1", "inbox_id": "hinb_1", "urgency": "critical", "class": "needs_you",
		"path": "/plans/1", "actions": "approve,review", "project": "project-a", "environment": "production", "sent_at": "2026-10-28T20:44:00Z", "revision": "3"}
	if len(got) != len(want) {
		t.Fatalf("keys: %v", got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("%s: got %q, want %q", k, got[k], v)
		}
	}
}

func phone(id string, settings map[string]string) *device.Device {
	return &device.Device{ID: id, Settings: settings}
}

func TestAllowDeviceLevels(t *testing.T) {
	cases := []struct {
		name       string
		class      string
		production bool
		settings   map[string]string
		want       bool
	}{
		{"staging critical lets an alert through", ClassAlert, false, nil, true},
		{"staging critical lets needs_you through", ClassNeedsYou, false, nil, true},
		{"staging critical holds an update", ClassUpdate, false, nil, false},
		{"production everything lets an update through", ClassUpdate, true, nil, true},
		{"production critical holds an update", ClassUpdate, true, map[string]string{"level.production": "critical"}, false},
		{"production off holds an alert", ClassAlert, true, map[string]string{"level.production": "off"}, false},
		{"staging off holds an alert", ClassAlert, false, map[string]string{"level.staging": "off"}, false},
		{"staging everything lets an update through", ClassUpdate, false, map[string]string{"level.staging": "everything"}, true},
		{"workspace ignores an off level", ClassWorkspace, true, map[string]string{"level.production": "off"}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := allowDevice(phone("hdev_a", c.settings), &Push{Class: c.class, Production: c.production}); got != c.want {
				t.Fatalf("allowed = %t, want %t", got, c.want)
			}
		})
	}
}

func TestAllowDeviceToggles(t *testing.T) {
	failed := &Push{Class: ClassAlert, Toggle: "push.failed"}
	if allowDevice(phone("hdev_a", map[string]string{"push.failed": "false"}), failed) {
		t.Fatal("a failure push reached a phone that turned failures off")
	}
	if !allowDevice(phone("hdev_a", nil), failed) {
		t.Fatal("a failure push is on by default")
	}
	live := &Push{Class: ClassUpdate, Toggle: "push.live", Production: true}
	if allowDevice(phone("hdev_a", nil), live) {
		t.Fatal("a live push is off by default")
	}
	if !allowDevice(phone("hdev_a", map[string]string{"push.live": "true"}), live) {
		t.Fatal("a live push didn't reach a phone that turned it on")
	}
}

func TestAllowDeviceExplicitDevices(t *testing.T) {
	p := &Push{Class: ClassUpdate, Toggle: "push.live", Devices: []string{"hdev_a"}}
	off := map[string]string{"level.staging": "off", "push.live": "false"}
	if !allowDevice(phone("hdev_a", off), p) {
		t.Fatal("a named phone is refused")
	}
	if allowDevice(phone("hdev_b", nil), p) {
		t.Fatal("an unnamed phone is allowed")
	}
}
