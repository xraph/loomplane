// Package qualify is the model qualification job. Once a week it deploys a
// rotating sample of registry models on one real GPU Droplet in a dedicated
// DigitalOcean account, probes each one the way a caller would, and records
// what passed and what failed. Those results are what lets the registry
// call a fit tested instead of estimated.
//
// The job spends real money, so it refuses to start without --confirm and
// the right account, checks a hard monthly cap and a per-run cap before the
// Droplet starts and before every sample, re-reads the month's spend every
// five minutes, and always deletes what it created. Every Droplet carries
// the loomplane-qualify tag and an expiry tag, and every SSH key it adds is
// named lpq-<run>, so a sweep finds anything a crashed run left.
package qualify

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/xraph/loomplane/pkg/provider"
)

// Config is .github/qualification.yaml. The account UUID isn't committed:
// the workflow passes it from a repository variable (see AccountEnv).
type Config struct {
	AccountUUID    string   `yaml:"account_uuid"`     // the dedicated DigitalOcean account; the token must belong to it
	Region         string   `yaml:"region"`           // "tor1"
	GPUKinds       []string `yaml:"gpu_kinds"`        // ["nvidia-rtx-4000-ada", "nvidia-l40s", "nvidia-h100-80gb"]
	MonthlyCapUSD  string   `yaml:"monthly_cap_usd"`  // "150.00"
	RunCapUSD      string   `yaml:"run_cap_usd"`      // "25.00"
	MaxRunHours    float64  `yaml:"max_run_hours"`    // 4
	SamplesPerRun  int      `yaml:"samples_per_run"`  // 8
	StaleAfterDays int      `yaml:"stale_after_days"` // 30
	// BalanceLagHours is how long after the account's last Droplet
	// activity a run waits, so DigitalOcean's balance holds the last run.
	// Zero is the default, 24.
	BalanceLagHours float64 `yaml:"balance_lag_hours"`
}

// defaultBalanceLag is BalanceLagHours when the config leaves it out.
const defaultBalanceLag = 24 * time.Hour

// BalanceLag is how long a run waits after the last Droplet activity.
func (c Config) BalanceLag() time.Duration {
	if c.BalanceLagHours == 0 {
		return defaultBalanceLag
	}
	return time.Duration(c.BalanceLagHours * float64(time.Hour))
}

// AccountEnv is the environment variable that holds the qualification
// account's UUID when the config file leaves account_uuid empty.
const AccountEnv = "QUALIFY_DO_ACCOUNT_UUID"

// maxRunHours keeps a run, plus its teardown, inside the workflow step's
// 300 minute limit, so the step after it still has time to sweep.
const maxRunHours = 4.5

// ErrInvalidConfig is a qualification config that breaks a rule.
var ErrInvalidConfig = errors.New("invalid qualification config")

// LoadConfig decodes a config strictly: an unknown field is an error. It
// doesn't validate; call Validate once the account UUID is in.
func LoadConfig(r io.Reader) (Config, error) {
	data, err := io.ReadAll(io.LimitReader(r, 1<<20))
	if err != nil {
		return Config{}, err
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var cfg Config
	if err := dec.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("%w: %w", ErrInvalidConfig, err)
	}
	return cfg, nil
}

// WithAccount fills AccountUUID from env when the file leaves it empty. A
// file and an environment that name different accounts is an error, so a
// stale variable can't point the job somewhere else.
func (c Config) WithAccount(env string) (Config, error) {
	env = strings.TrimSpace(env)
	switch {
	case env == "":
	case c.AccountUUID == "":
		c.AccountUUID = env
	case c.AccountUUID != env:
		return c, fmt.Errorf("%w: account_uuid and %s name different accounts", ErrInvalidConfig, AccountEnv)
	}
	return c, nil
}

// Validate checks every field. A cap that isn't a positive number of
// dollars is an error, never "no cap".
func (c Config) Validate() error {
	var problems []string
	if strings.TrimSpace(c.AccountUUID) == "" {
		problems = append(problems, "account_uuid is empty; set it or "+AccountEnv+" to the qualification account's UUID")
	}
	if c.Region == "" {
		problems = append(problems, "region is required")
	}
	if len(c.GPUKinds) == 0 {
		problems = append(problems, "gpu_kinds needs at least one GPU kind")
	}
	for _, k := range c.GPUKinds {
		if a, ok := provider.LookupAcceleratorKind(k); !ok || a.Vendor != provider.VendorNVIDIA {
			problems = append(problems, fmt.Sprintf("gpu_kinds: %q isn't an NVIDIA GPU kind", k))
		}
	}
	monthly, mErr := parseUSD(c.MonthlyCapUSD)
	if mErr != nil {
		problems = append(problems, "monthly_cap_usd: "+mErr.Error())
	}
	run, rErr := parseUSD(c.RunCapUSD)
	if rErr != nil {
		problems = append(problems, "run_cap_usd: "+rErr.Error())
	}
	if mErr == nil && rErr == nil && run > monthly {
		problems = append(problems, "run_cap_usd can't be more than monthly_cap_usd")
	}
	if c.MaxRunHours <= 0 || c.MaxRunHours > maxRunHours {
		problems = append(problems, fmt.Sprintf("max_run_hours must be more than 0 and at most %g", maxRunHours))
	}
	if c.SamplesPerRun < 1 {
		problems = append(problems, "samples_per_run must be at least 1")
	}
	if c.StaleAfterDays < 1 {
		problems = append(problems, "stale_after_days must be at least 1")
	}
	if c.BalanceLagHours < 0 || math.IsNaN(c.BalanceLagHours) || c.BalanceLagHours > 24*31 {
		problems = append(problems, "balance_lag_hours must be from 0 (the default, 24) to 744")
	}
	if len(problems) > 0 {
		return fmt.Errorf("%w: %s", ErrInvalidConfig, strings.Join(problems, "; "))
	}
	return nil
}

// caps returns the monthly and per-run caps in dollars.
func (c Config) caps() (monthly, run float64, err error) {
	if monthly, err = parseUSD(c.MonthlyCapUSD); err != nil {
		return 0, 0, fmt.Errorf("%w: monthly_cap_usd: %w", ErrInvalidConfig, err)
	}
	if run, err = parseUSD(c.RunCapUSD); err != nil {
		return 0, 0, fmt.Errorf("%w: run_cap_usd: %w", ErrInvalidConfig, err)
	}
	return monthly, run, nil
}

// parseUSD reads a positive dollar amount such as "150.00".
func parseUSD(s string) (float64, error) {
	v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil || v <= 0 || math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, fmt.Errorf("%q isn't a positive amount of dollars", s)
	}
	return v, nil
}

// usd shows an amount to the cent.
func usd(v float64) string { return "$" + strconv.FormatFloat(v, 'f', 2, 64) }
