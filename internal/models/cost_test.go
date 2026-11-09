package models

import (
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/gpus"
	"github.com/xraph/loomplane/pkg/provider"
)

func target(source string, usd float64, known, estimate bool, count int) gpus.GPUTarget {
	return gpus.GPUTarget{
		Source: source, HourlyUSD: usd, HourlyPriceKnown: known, PriceEstimate: estimate,
		Accelerator: provider.Accelerator{Kind: "nvidia-h100-80gb", Count: count},
	}
}

func TestHourlyCostShare(t *testing.T) {
	server := HourlyCost(target("connection", 3.39, true, false, 1), 1, "")
	want := Cost{HourlyUSD: "3.3900", Known: true, Source: "server", Share: "1 of 1 GPUs", MonthlyUSD: "2474.70"}
	if server != want {
		t.Fatalf("server: %+v, want %+v", server, want)
	}
	cluster := HourlyCost(target("connection", 1.40, true, true, 2), 1, "")
	want = Cost{HourlyUSD: "0.7000", Known: true, Estimate: true, Source: "cluster", Share: "1 of 2 GPUs", MonthlyUSD: "511.00"}
	if cluster != want {
		t.Fatalf("cluster: %+v, want %+v", cluster, want)
	}
	// A float that isn't exact lands on the nearest ten-thousandth.
	if got := HourlyCost(target("connection", 0.1+0.2, true, false, 1), 1, ""); got.HourlyUSD != "0.3000" {
		t.Fatalf("0.1+0.2: %q", got.HourlyUSD)
	}
	// A third of 1.00 rounds half up at the fourth place.
	if got := HourlyCost(target("connection", 1, true, false, 3), 1, ""); got.HourlyUSD != "0.3333" {
		t.Fatalf("a third: %q", got.HourlyUSD)
	}
}

func TestUnknownPriceIsNeverZero(t *testing.T) {
	for name, tg := range map[string]gpus.GPUTarget{
		"price unknown":  target("connection", 0, false, false, 1),
		"price is zero":  target("connection", 0, false, true, 2),
		"a server size":  target("server_size", 3.39, true, false, 1),
		"no GPU count":   target("connection", 3.39, true, false, 0),
		"a negative one": target("connection", -1, true, false, 1),
	} {
		got := HourlyCost(tg, 1, "")
		if got.HourlyUSD != "" || got.Known || got.MonthlyUSD != "" || got.Source != "unknown" {
			t.Errorf("%s: %+v", name, got)
		}
	}
}

func TestOverrideWins(t *testing.T) {
	got := HourlyCost(target("connection", 0, false, false, 2), 1, "2.5")
	want := Cost{HourlyUSD: "2.5000", Known: true, Source: "override", Share: "1 of 2 GPUs", MonthlyUSD: "1825.00"}
	if got != want {
		t.Fatalf("override over unknown: %+v, want %+v", got, want)
	}
	if got = HourlyCost(target("connection", 3.39, true, true, 1), 1, "1"); got.HourlyUSD != "1.0000" || got.Estimate {
		t.Fatalf("override over a known estimate: %+v", got)
	}
	// An override the store would have refused is ignored.
	if got = HourlyCost(target("connection", 3.39, true, false, 1), 1, "lots"); got.Source != "server" {
		t.Fatalf("a bad override: %+v", got)
	}
}

func TestCPUCostIsPartOfTheServers(t *testing.T) {
	got := HourlyCost(target("connection", 3.39, true, false, 1), 0, "")
	if got.Source != "cpu" || !got.Known || got.HourlyUSD != "0.00" {
		t.Fatalf("cpu: %+v", got)
	}
}

func TestHoursInMonth(t *testing.T) {
	for _, tt := range []struct {
		at   time.Time
		want float64
	}{
		{time.Date(2026, 2, 10, 0, 0, 0, 0, time.UTC), 672},
		{time.Date(2026, 11, 30, 23, 0, 0, 0, time.UTC), 720},
		{time.Date(2028, 2, 1, 0, 0, 0, 0, time.UTC), 696},
		{time.Date(2026, 12, 31, 20, 0, 0, 0, time.FixedZone("x", -5*3600)), 744}, // Jan 1 UTC
	} {
		if got := HoursInMonth(tt.at); got != tt.want {
			t.Errorf("%v: %v, want %v", tt.at, got, tt.want)
		}
	}
}
