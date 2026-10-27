package registry

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/xraph/loomplane/pkg/provider"
)

// CPUKind is the GPU kind a qualification result names when it ran on CPU.
const CPUKind = "cpu"

// The outcomes a qualification result records.
const (
	OutcomePassed = "passed"
	OutcomeFailed = "failed"
)

// Status says how much Loomplane knows about a fit.
type Status string

// The statuses a fit can have. Only a passed qualification run on the
// engine version Loomplane ships makes a fit tested; the memory math alone
// makes it estimated.
const (
	StatusTested    Status = "tested"    // a qualification run deployed and served it
	StatusEstimated Status = "estimated" // the memory math says it fits; no run proved it
	StatusFailed    Status = "failed"    // a qualification run failed; never offered
	StatusUntested  Status = "untested"  // not a registry entry, so no run will ever check it
	StatusNoFit     Status = "no-fit"    // the memory math says it doesn't fit
)

// Result is one qualification run of one quantization on one GPU kind.
type Result struct {
	Entry         string    `yaml:"entry" json:"entry"`
	Quantization  string    `yaml:"quantization" json:"quantization"`
	Revision      string    `yaml:"revision" json:"revision"` // the weights revision deployed, from Quantization.WeightsRepo
	GPUKind       string    `yaml:"gpu_kind" json:"gpu_kind"` // a P1 kind ID, or "cpu"
	GPUs          int       `yaml:"gpus" json:"gpus"`
	MaxModelLen   int       `yaml:"max_model_len" json:"max_model_len"`
	Engine        Engine    `yaml:"engine" json:"engine"`
	EngineVersion string    `yaml:"engine_version" json:"engine_version"` // release Product, "0.31.0"
	Outcome       string    `yaml:"outcome" json:"outcome"`               // "passed" or "failed"
	Detail        string    `yaml:"detail" json:"detail"`                 // "first token 210 ms, 61 tokens/s" or the failure
	RanAt         time.Time `yaml:"ran_at" json:"ran_at"`
	Run           string    `yaml:"run" json:"run"` // GitHub run URL
}

// Fit is where a quantization fits on one GPU kind, and how sure Loomplane
// is of it.
type Fit struct {
	GPUKind string  `json:"gpu_kind"`
	Need    Need    `json:"need"`
	Status  Status  `json:"status"`
	Result  *Result `json:"result,omitempty"` // the counting result for tested and failed
	Reason  string  `json:"reason,omitempty"` // no-fit and failed
}

// Fits reports whether the fit can be offered: tested, estimated or
// untested. A failed or no-fit one never can.
func (f Fit) Fits() bool {
	return f.Status == StatusTested || f.Status == StatusEstimated || f.Status == StatusUntested
}

// qualification is the shape of data/qualification.yaml.
type qualification struct {
	Results []Result `yaml:"results"`
}

// Results returns every qualification result, in file order. The results
// are copies.
func (r *Registry) Results() []Result {
	return append([]Result(nil), r.results...)
}

// FitOn combines the estimate with the newest result whose EngineVersion
// equals current[q.Engine]. A failed result makes the fit StatusFailed and
// never fitting, whatever the estimate says.
//
// kind is a P1 GPU kind, or CPUKind for TEI and llama.cpp on CPU. A result
// only counts when it deployed the weights revision q runs today, so a
// re-pinned entry starts over as estimated. When the estimate fits, the
// newest result on the offered GPU count decides, skipping passed runs at a
// shorter context than the one served: they prove nothing about it, and a
// failed run behind them still wins. An entry that isn't in r gets
// StatusUntested when it fits.
func (r *Registry) FitOn(e Entry, q Quantization, kind string, maxCount int, current map[Engine]string) Fit {
	var need Need
	var reason string
	var ok bool
	if kind == CPUKind {
		need, reason, ok = CPUFits(e, q)
	} else {
		need, reason, ok = Smallest(e, q, kind, maxCount)
	}
	fit := Fit{GPUKind: kind, Need: need}

	if _, listed := r.Entry(e.ID); !listed {
		if ok {
			fit.Status = StatusUntested
		} else {
			fit.Status, fit.Reason = StatusNoFit, reason
		}
		return fit
	}

	version := current[q.Engine]
	_, revision := q.WeightsRepo(e)
	var newest *Result
	for i := range r.results {
		res := &r.results[i]
		if version == "" || res.Entry != e.ID || res.Quantization != q.ID || res.Revision != revision || res.GPUKind != kind ||
			res.Engine != q.Engine || res.EngineVersion != version {
			continue
		}
		// When it fits, only a run on the offered GPU count says anything
		// about this fit. When it doesn't, a failed run on any count the
		// target has explains it better than the estimate does.
		if ok && (res.GPUs != need.GPUs || (res.Outcome == OutcomePassed && res.MaxModelLen < need.MaxModelLen)) {
			continue
		}
		if !ok && (res.Outcome != OutcomeFailed || (kind != CPUKind && res.GPUs > maxCount)) {
			continue
		}
		if newest == nil || !res.RanAt.Before(newest.RanAt) {
			newest = res
		}
	}

	switch {
	case newest != nil && newest.Outcome == OutcomeFailed:
		res := *newest
		fit.Status, fit.Result, fit.Reason = StatusFailed, &res, failedReason(res)
	case !ok:
		fit.Status, fit.Reason = StatusNoFit, reason
	case newest != nil:
		res := *newest
		fit.Status, fit.Result = StatusTested, &res
	default:
		fit.Status = StatusEstimated
	}
	return fit
}

func failedReason(res Result) string {
	if res.Detail != "" {
		return res.Detail
	}
	return fmt.Sprintf("Loomplane's qualification run failed on this GPU with %s %s.", engineTitle(res.Engine), res.EngineVersion)
}

// readQualification reads data/qualification.yaml, when fsys has one, and
// checks every result against the loaded entries.
func readQualification(fsys fs.FS, r *Registry) ([]Result, error) {
	data, err := fs.ReadFile(fsys, qualificationFile)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("registry: %w", err)
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var f qualification
	if err := dec.Decode(&f); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, invalidf("%s: the file is empty", qualificationFile)
		}
		return nil, &invalidError{msg: qualificationFile + ": " + err.Error(), cause: err}
	}
	var extra yaml.Node
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, invalidf("%s: holds more than one YAML document", qualificationFile)
	}

	var problems []error
	for i, res := range f.Results {
		for _, p := range resultProblems(r, res) {
			problems = append(problems, invalidf("%s: result %d (%s %s on %s): %s", qualificationFile, i+1, res.Entry, res.Quantization, res.GPUKind, p))
		}
	}
	if len(problems) > 0 {
		return nil, errors.Join(problems...)
	}
	return f.Results, nil
}

// resultProblems lists what's wrong with one qualification result.
func resultProblems(r *Registry, res Result) []string {
	var out []string
	if i, ok := r.byID[res.Entry]; !ok {
		out = append(out, "no registry entry has that ID")
	} else if q, ok := r.entries[i].Quantization(res.Quantization); !ok {
		out = append(out, "the entry has no such quantization")
	} else if q.Engine != res.Engine {
		out = append(out, fmt.Sprintf("engine %q, but the quantization runs on %s", res.Engine, q.Engine))
	}
	if !revisionPattern.MatchString(res.Revision) {
		out = append(out, fmt.Sprintf("revision %q isn't a 40-character commit SHA", res.Revision))
	}
	if res.GPUKind == CPUKind {
		if res.GPUs != 0 {
			out = append(out, "a CPU run has gpus 0")
		}
	} else {
		if _, ok := provider.LookupAcceleratorKind(res.GPUKind); !ok {
			out = append(out, "gpu_kind isn't a known GPU kind or \"cpu\"")
		}
		if !validTensorParallel(res.GPUs) {
			out = append(out, "gpus must be 1, 2, 4 or 8")
		}
	}
	if res.MaxModelLen <= 0 {
		out = append(out, "max_model_len must be positive")
	}
	if res.EngineVersion == "" {
		out = append(out, "engine_version is required")
	}
	if res.Outcome != OutcomePassed && res.Outcome != OutcomeFailed {
		out = append(out, fmt.Sprintf("outcome %q isn't passed or failed", res.Outcome))
	}
	if res.RanAt.IsZero() {
		out = append(out, "ran_at is required")
	}
	return out
}
