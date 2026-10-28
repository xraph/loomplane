package models

import (
	"fmt"
	"math"
	"time"

	"github.com/xraph/loomplane/internal/gpus"
)

// Where a Cost's price came from.
const (
	CostSourceServer   = "server"   // a connected server's price
	CostSourceCluster  = "cluster"  // a cluster node's list price
	CostSourceOverride = "override" // what the owner says it costs
	CostSourceCPU      = "cpu"      // no GPU: part of the server's own cost
	CostSourceUnknown  = "unknown"  // nobody told us
)

// monthlyHours is the 730 hours the server editor prices a month at.
const monthlyHours = 730

// Cost is what a model's GPUs cost per hour. Money is decimal text and an
// unknown price is an empty string, never "0.00".
//
// Source "cpu" with HourlyUSD "0.00" doesn't mean the model is free. It
// means the model runs on CPU and its cost is part of its server's, so a
// page shows that sentence, never "$0.00".
type Cost struct {
	HourlyUSD  string `json:"hourly_usd"` // four places, "" when unknown
	Known      bool   `json:"known"`
	Estimate   bool   `json:"estimate"`    // list price of a cluster node
	Source     string `json:"source"`      // one of the CostSource constants
	Share      string `json:"share"`       // "1 of 2 GPUs"
	MonthlyUSD string `json:"monthly_usd"` // hourly x 730, two places, "" when unknown
}

// HourlyCost attributes the GPU host's price by the share of its GPUs the
// model holds. An override wins. A model that holds no GPU runs on CPU, and
// its cost is part of its server's. A host without a known price is unknown.
//
// Pass gpusHeld 0 only for a CPU engine (TEI, or llama.cpp on CPU). A GPU
// model whose held count isn't known yet, say one that isn't scheduled,
// has an unknown cost (Source "unknown"). Don't call this with 0 for it:
// the result would say Source "cpu" and "0.00", which looks free.
//
// The target's float price is converted once, here, to whole
// ten-thousandths of a dollar, rounding half away from zero, and everything
// after that is integer arithmetic. The share is rounded half up to the
// ten-thousandth, and the monthly figure half up to the cent.
func HourlyCost(target gpus.GPUTarget, gpusHeld int, override string) Cost {
	if override != "" {
		// The store checks an override when it's saved; one that doesn't
		// parse is ignored rather than trusted.
		if units, err := overrideAmount.parse(override); err == nil {
			return Cost{
				HourlyUSD: formatUSD(units), Known: true, Source: CostSourceOverride,
				Share: gpuShare(gpusHeld, target.Accelerator.Count), MonthlyUSD: monthlyUSD(units),
			}
		}
	}
	if gpusHeld <= 0 {
		return Cost{HourlyUSD: "0.00", Known: true, Source: CostSourceCPU, MonthlyUSD: "0.00"}
	}
	count := target.Accelerator.Count
	if target.Source != "connection" || !target.HourlyPriceKnown || count <= 0 ||
		math.IsNaN(target.HourlyUSD) || math.IsInf(target.HourlyUSD, 0) || target.HourlyUSD < 0 {
		return Cost{Source: CostSourceUnknown, Share: gpuShare(gpusHeld, count)}
	}
	host := int64(math.Round(target.HourlyUSD * unitsPerUSD))
	held := int64(min(gpusHeld, count))
	units := (host*held + int64(count)/2) / int64(count)
	source := CostSourceServer
	if target.PriceEstimate {
		source = CostSourceCluster
	}
	return Cost{
		HourlyUSD: formatUSD(units), Known: true, Estimate: target.PriceEstimate, Source: source,
		Share: gpuShare(gpusHeld, count), MonthlyUSD: monthlyUSD(units),
	}
}

// wholeServerCost is what all of host's GPUs cost, for a model that holds
// own of them and is the only workload there. ok is false when anything
// else holds a GPU of host, or host isn't a server or cluster with GPUs.
//
// A server Loomplane created for a model is that model's, whatever share
// of its GPUs the engine holds, until another workload takes a GPU there
// (Ruling M42). own is what the model's deployed engine holds, 0 before
// it's deployed, since host.Free counts deployed workloads only.
func wholeServerCost(host gpus.GPUTarget, own int, override string) (Cost, bool) {
	count := host.Accelerator.Count
	if host.Source != gpus.SourceConnection || count <= 0 || count-host.Free-own > 0 {
		return Cost{}, false
	}
	return HourlyCost(host, count, override), true
}

// ownsServer reports whether Loomplane created the server d's model runs
// on, for d. While d moves onto a new server, that's the server it runs on
// until the move finishes, which the move recorded.
func ownsServer(d *Deployment) bool {
	if d == nil {
		return false
	}
	if waitsForServer(d) && d.Request != nil && d.Request.Moving != nil {
		return d.Request.Moving.ServerCloudID != ""
	}
	return d.ServerCloudID != ""
}

// gpuShare says how much of the host a model holds: "1 of 2 GPUs".
func gpuShare(held, count int) string {
	if held <= 0 || count <= 0 {
		return ""
	}
	return fmt.Sprintf("%d of %d GPUs", min(held, count), count)
}

// monthlyUSD is units an hour for 730 hours, to the cent, half up.
func monthlyUSD(units int64) string {
	cents := (units*monthlyHours + unitsPerUSD/200) / (unitsPerUSD / 100)
	return fmt.Sprintf("%d.%02d", cents/100, cents%100)
}

// HoursInMonth is the hours in t's calendar month, in UTC.
func HoursInMonth(t time.Time) float64 {
	t = t.UTC()
	first := time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
	return first.AddDate(0, 1, 0).Sub(first).Hours()
}
