package qualify

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"sort"
	"time"

	"github.com/xraph/loomplane/internal/models/engines"
	"github.com/xraph/loomplane/internal/models/registry"
)

// Sample is one quantization of one entry, deployed on one GPU kind (or on
// the Droplet's CPUs, as registry.CPUKind) with a GPU count.
type Sample struct {
	Entry        string `json:"entry"`
	Quantization string `json:"quantization"`
	GPUKind      string `json:"gpu_kind"` // the run's GPU kind, or "cpu"
	GPUs         int    `json:"gpus"`     // 0 on CPU
}

func (s Sample) String() string {
	return fmt.Sprintf("%s %s on %s", s.Entry, s.Quantization, s.where())
}

func (s Sample) where() string {
	if s.GPUKind == registry.CPUKind {
		return "CPU"
	}
	return fmt.Sprintf("%d %s", s.GPUs, s.GPUKind)
}

// DropletGPUs is how many GPUs the run's Droplet has. A run buys the
// smallest Droplet of its kind, so only what fits one GPU is sampled.
const DropletGPUs = 1

// The run's time budget. These are estimates for planning, never spend
// limits: the caps are what stop spending.
const (
	// SetupHours is the Droplet's boot, the NVIDIA toolkit and the first
	// engine image pulls.
	SetupHours = 0.5
	// sampleOverheadHours is a sample's start, probe and teardown.
	sampleOverheadHours = 0.25
	// downloadBytesPerHour assumes weights arrive at 50 MB/s.
	downloadBytesPerHour = 50e6 * 3600
)

// SampleHours estimates how long one sample of q takes.
func SampleHours(q registry.Quantization) float64 {
	return sampleOverheadHours + float64(q.WeightBytes)/downloadBytesPerHour
}

// CurrentWeek is t's ISO week, "2026-W45".
func CurrentWeek(t time.Time) string {
	y, w := t.UTC().ISOWeek()
	return fmt.Sprintf("%d-W%02d", y, w)
}

// ParseWeek reads an ISO week, "2026-W45", and returns its number and the
// Monday it starts on, at 00:00 UTC.
func ParseWeek(week string) (int, time.Time, error) {
	var y, w int
	if n, err := fmt.Sscanf(week, "%d-W%d", &y, &w); err != nil || n != 2 || CurrentWeek(isoMonday(y, w)) != week {
		return 0, time.Time{}, fmt.Errorf("%q isn't an ISO week like 2026-W45", week)
	}
	return w, isoMonday(y, w), nil
}

// isoMonday is the Monday of ISO week w of year y. January 4th is always
// in week 1.
func isoMonday(y, w int) time.Time {
	jan4 := time.Date(y, time.January, 4, 0, 0, 0, 0, time.UTC)
	offset := (int(jan4.Weekday()) + 6) % 7 // days since Monday
	return jan4.AddDate(0, 0, -offset+(w-1)*7)
}

// Pick chooses this run's GPU kind (ISO week mod len(GPUKinds)) and samples: core entries first whose current-engine
// result on that kind is missing or older than StaleAfterDays, then non-core entries ordered by sha256(entry + ISO week),
// all fitting one Droplet of that kind, up to SamplesPerRun and the run's hours.
//
// A sample's result is current when it ran on the engine release Loomplane
// ships, on the weights revision the entry pins today, on the same GPU
// kind and count, at no shorter a context than the memory math serves:
// exactly what registry.FitOn counts. A re-pinned entry has no current
// result, so it's picked again. Non-core entries with a current result are
// skipped too, so the hash only rotates through what still needs a run.
// TEI, and llama.cpp on CPU, run on the Droplet's CPUs as registry.CPUKind.
// A bad week or no GPU kinds picks nothing.
func Pick(r *registry.Registry, results []registry.Result, cfg Config, week string) (kind string, samples []Sample) {
	number, monday, err := ParseWeek(week)
	if err != nil || len(cfg.GPUKinds) == 0 {
		return "", nil
	}
	kind = cfg.GPUKinds[number%len(cfg.GPUKinds)]
	current := engines.Current()
	staleBefore := monday.AddDate(0, 0, -cfg.StaleAfterDays)

	type candidate struct {
		sample Sample
		core   bool
		rank   int // registry order
		order  [sha256.Size]byte
		hours  float64
	}
	var candidates []candidate
	for i, e := range r.Entries() {
		order := sha256.Sum256([]byte(e.ID + week))
		for _, q := range e.Quantizations {
			for _, s := range samplesOn(e, q, kind) {
				need, err := sampleNeed(e, q, s)
				if err != nil || hasCurrent(results, e, q, s, need, current, staleBefore) {
					continue
				}
				candidates = append(candidates, candidate{sample: s, core: e.Core, rank: i, order: order, hours: SampleHours(q)})
			}
		}
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		a, b := candidates[i], candidates[j]
		if a.core != b.core {
			return a.core
		}
		if a.core {
			return a.rank < b.rank
		}
		if c := bytes.Compare(a.order[:], b.order[:]); c != 0 {
			return c < 0
		}
		return a.rank < b.rank
	})

	budget := cfg.MaxRunHours - SetupHours
	for _, c := range candidates {
		if len(samples) >= cfg.SamplesPerRun {
			break
		}
		if c.hours > budget {
			continue
		}
		budget -= c.hours
		samples = append(samples, c.sample)
	}
	return kind, samples
}

// samplesOn lists where q of e can be qualified on a Droplet of kind: on
// its GPU when the memory math fits one, and on its CPUs for TEI and
// llama.cpp when the CPU math fits.
func samplesOn(e registry.Entry, q registry.Quantization, kind string) []Sample {
	var out []Sample
	if q.Engine == registry.EngineVLLM || q.Engine == registry.EngineLlamaCPP {
		if need, _, ok := registry.Smallest(e, q, kind, DropletGPUs); ok {
			out = append(out, Sample{Entry: e.ID, Quantization: q.ID, GPUKind: kind, GPUs: need.GPUs})
		}
	}
	if q.Engine == registry.EngineTEI || q.Engine == registry.EngineLlamaCPP {
		if _, _, ok := registry.CPUFits(e, q); ok {
			out = append(out, Sample{Entry: e.ID, Quantization: q.ID, GPUKind: registry.CPUKind})
		}
	}
	return out
}

// sampleNeed is what the memory math serves s at: the context and GPU
// count a passed result must match for registry.FitOn to count it.
func sampleNeed(e registry.Entry, q registry.Quantization, s Sample) (registry.Need, error) {
	var (
		need   registry.Need
		reason string
		ok     bool
	)
	if s.GPUKind == registry.CPUKind {
		need, reason, ok = registry.CPUFits(e, q)
	} else {
		need, reason, ok = registry.Estimate(e, q, s.GPUKind, s.GPUs)
	}
	if !ok {
		return need, fmt.Errorf("%s doesn't fit: %s", s, reason)
	}
	return need, nil
}

// hasCurrent reports whether results hold a run of s that registry.FitOn
// would count today and that ran after staleBefore.
func hasCurrent(results []registry.Result, e registry.Entry, q registry.Quantization, s Sample, need registry.Need, current map[registry.Engine]string, staleBefore time.Time) bool {
	version := current[q.Engine]
	_, revision := q.WeightsRepo(e)
	for _, res := range results {
		if res.Entry != e.ID || res.Quantization != q.ID || res.Revision != revision || res.GPUKind != s.GPUKind || res.GPUs != s.GPUs ||
			res.Engine != q.Engine || version == "" || res.EngineVersion != version {
			continue
		}
		if res.Outcome == registry.OutcomePassed && res.MaxModelLen < need.MaxModelLen {
			continue
		}
		if !res.RanAt.Before(staleBefore) {
			return true
		}
	}
	return false
}

// PlannedHours is the run's estimated length for samples: setup plus each
// sample. The worst case a run is guarded with is always MaxRunHours.
func PlannedHours(r *registry.Registry, samples []Sample) float64 {
	hours := SetupHours
	for _, s := range samples {
		if e, ok := r.Entry(s.Entry); ok {
			if q, ok := e.Quantization(s.Quantization); ok {
				hours += SampleHours(q)
			}
		}
	}
	return hours
}
