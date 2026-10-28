package models

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/security"
)

// TestPrincipalIsPrivate: who asked, what they asked and which budget
// notices went out are kept on the row but never sent.
func TestPrincipalIsPrivate(t *testing.T) {
	d := Deployment{
		ID: "aimodel_1", OrgID: "org_secret", ProjectID: "proj_1", EnvironmentID: "env_1", Resource: "llama",
		EntryID: "meta-llama/Llama-3.1-8B-Instruct", Quantization: "bf16", Engine: "vllm", Phase: PhaseWaitingForServer,
		ServerName: "gpu-box", Request: &DeployInput{EntryID: "meta-llama/Llama-3.1-8B-Instruct", MonthlyBudgetUSD: "250.00"},
		RequestedBy:      security.Principal{UserID: "usr_requester", OrgID: "org_secret", KeyID: "key_requester"},
		AlertedProjected: "2026-11", AlertedCrossed: "2026-10",
		Month: "2026-11", MonthCostUSD: "4.7700", AccruedAt: time.Date(2026, 11, 3, 0, 0, 0, 0, time.UTC),
	}
	raw, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	got := string(raw)
	for _, leak := range []string{"requested_by", "usr_requester", "key_requester", "org_secret", "org_id", "request\"", "alerted", "UserID"} {
		if strings.Contains(got, leak) {
			t.Errorf("JSON has %q: %s", leak, got)
		}
	}
	for _, want := range []string{`"server_name":"gpu-box"`, `"month_cost_usd":"4.7700"`, `"phase":"waiting_for_server"`} {
		if !strings.Contains(got, want) {
			t.Errorf("JSON lacks %s: %s", want, got)
		}
	}
	// Empty optional fields are left out, so "no budget" never reads as $0.
	if strings.Contains(got, "monthly_budget_usd") || strings.Contains(got, "hourly_usd_override") {
		t.Errorf("JSON has an empty amount: %s", got)
	}
}

func TestMoneyIsDecimalText(t *testing.T) {
	for _, tt := range []struct {
		in    string
		units int64
	}{
		{"0", 0}, {"3.20", 32000}, {"1.57", 15700}, {"0.0001", 1}, {"12345678.9999", 123456789999}, {"007.5", 75000},
	} {
		got, err := costAmount.parse(tt.in)
		if err != nil || got != tt.units {
			t.Errorf("parse(%q) = %d, %v; want %d", tt.in, got, err, tt.units)
		}
	}
	for _, in := range []string{"", "-1", "1.", ".5", "1e3", "1.23456", "123456789", "NaN", " 1", "1,00"} {
		if _, err := costAmount.parse(in); !errors.Is(err, ErrInvalid) {
			t.Errorf("parse(%q): err = %v, want ErrInvalid", in, err)
		}
	}
	if _, err := budgetAmount.parse("10.005"); !errors.Is(err, ErrInvalid) {
		t.Errorf("a budget with three places: err = %v, want ErrInvalid", err)
	}
	if _, err := overrideAmount.parse("1000000"); !errors.Is(err, ErrInvalid) {
		t.Errorf("an override past NUMERIC(10,4): err = %v, want ErrInvalid", err)
	}
	for units, want := range map[int64]string{0: "0.0000", 1: "0.0001", 47700: "4.7700", 123456789999: "12345678.9999"} {
		if got := formatUSD(units); got != want {
			t.Errorf("formatUSD(%d) = %q, want %q", units, got, want)
		}
	}
}

func TestValidateRefusesWhatTheTableWould(t *testing.T) {
	good := func() *Deployment {
		return &Deployment{OrgID: "org_a", ProjectID: "proj_1", EnvironmentID: "env_1", Resource: "llama",
			EntryID: "e", Quantization: "bf16", Engine: "vllm", Phase: PhasePlanned}
	}
	if err := good().validate(); err != nil {
		t.Fatalf("a good row: %v", err)
	}
	for name, change := range map[string]func(*Deployment){
		"no org":           func(d *Deployment) { d.OrgID = "" },
		"no environment":   func(d *Deployment) { d.EnvironmentID = "" },
		"no engine":        func(d *Deployment) { d.Engine = "" },
		"unknown phase":    func(d *Deployment) { d.Phase = "running" },
		"zero budget":      func(d *Deployment) { d.MonthlyBudgetUSD = "0.00" },
		"text budget":      func(d *Deployment) { d.MonthlyBudgetUSD = "lots" },
		"negative hourly":  func(d *Deployment) { d.HourlyUSDOverride = "-1" },
		"too many decimal": func(d *Deployment) { d.HourlyUSDOverride = "0.12345" },
	} {
		d := good()
		change(d)
		if err := d.validate(); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", name, err)
		}
	}
	d := good()
	d.HourlyUSDOverride = "0" // the owner says it's free: known, not unknown
	if err := d.validate(); err != nil {
		t.Errorf("a zero override: %v", err)
	}
}
