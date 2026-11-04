package platformcloud

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"time"

	fc "github.com/xraph/loomplane/internal/firecracker"
	"github.com/xraph/loomplane/internal/metering"
)

// Features the platform Cloud meters. Every event is PlatformOwned and names
// the placement, never a host.
const (
	FeatureComputeVCPUHours  = "compute_vcpu_hours"
	FeatureComputeGBRAMHours = "compute_gb_ram_hours"
	FeatureBuildMinutes      = "build_minutes"
	FeatureEgressGB          = "egress_gb"
)

// maxCatchUp bounds how far back one sample may bill. A placement running
// through a sampler outage is billed for all of it, up to this long; past it
// the dropped minutes are logged with the placement ID.
const maxCatchUp = 24 * time.Hour

// emitDelay keeps an hour open for a little after it ends, so a sample that
// was already in flight when the hour closed still lands in it.
const emitDelay = 2 * time.Minute

// WithMeter sets where usage is recorded and returns the service. Nil drops
// it.
func (s *Service) WithMeter(m metering.Meter) *Service {
	s.meter = m
	return s
}

func (s *Service) meterOrDiscard() metering.Meter {
	if s.meter == nil {
		return metering.Discard
	}
	return s.meter
}

func hourOf(t time.Time) time.Time { return t.UTC().Truncate(time.Hour) }

func hourID(t time.Time) string { return hourOf(t).Format(time.RFC3339) }

// lockMeterRow serializes samplers of one placement for the transaction.
func lockMeterRow(ctx context.Context, tx driverTx, key string) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, key)
	return err
}

// SampleCompute counts the minute ending at now for every running service
// placement and emits the compute events of every hour that has closed.
//
// Minutes accumulate in loomplane_platform_meter_compute, one row per
// placement, generation and hour, and an hour's two events are emitted only
// once the hour is over, so each carries the hour's whole total and its ID
// (platform:<placement>:<generation>:<hour RFC3339>:<feature>) never needs a
// second, larger event. The row remembers the last minute counted: a sample
// for a minute already counted adds nothing, so a restarted sampler repeats
// no minute. A sample after a gap adds the minutes in between, but never from
// before the placement last went into running (placements.running_since), so
// a suspension is not billed and a sampler outage is. A resize makes a new
// placement, so vCPUs and memory never change under a row's key.
func (s *Service) SampleCompute(ctx context.Context, now time.Time) error {
	minute := now.UTC().Truncate(time.Minute)
	running, err := queryPlacements(ctx, s.store.db, `kind='service' AND state='running'`)
	if err != nil {
		return err
	}
	var errs []error
	for _, p := range running {
		if cErr := s.countMinutes(ctx, p, minute); cErr != nil {
			errs = append(errs, fmt.Errorf("count placement %s: %w", p.ID, cErr))
		}
	}
	errs = append(errs, s.emitComputeHours(ctx, now))
	return errors.Join(errs...)
}

// countMinutes adds the minutes placement p ran, up to minute, to their
// hours. Minute labels count the minute ending at the label. Counting starts
// after the later of the last minute counted and the start of the minute the
// placement last went into running, so time spent suspended or stopped is
// never billed and a sampler outage while running is billed in full.
func (s *Service) countMinutes(ctx context.Context, p Placement, minute time.Time) error {
	return s.store.withTx(ctx, func(tx driverTx) error {
		if err := lockMeterRow(ctx, tx, "compute:"+p.ID); err != nil {
			return err
		}
		var last, since *time.Time
		if err := tx.QueryRow(ctx, `SELECT max(last_minute) FROM loomplane_platform_meter_compute WHERE placement_id=$1 AND generation=$2`,
			p.ID, p.Generation).Scan(&last); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT running_since FROM loomplane_platform_placements WHERE id=$1 AND state='running'`, p.ID).Scan(&since); err != nil {
			if errors.Is(storeError(err), ErrNotFound) {
				return nil // stopped since the list was read
			}
			return err
		}
		from := minute.Add(-time.Minute) // a placement with no start time counts this minute only
		if since != nil {
			from = since.UTC().Truncate(time.Minute)
		}
		if last != nil && last.UTC().After(from) {
			from = last.UTC()
		}
		if !minute.After(from) {
			return nil
		}
		if minute.Sub(from) > maxCatchUp {
			dropped := int((minute.Sub(from) - maxCatchUp) / time.Minute)
			slog.WarnContext(ctx, "platform meter: minutes dropped past the catch-up limit", "placement", p.ID, "minutes", dropped)
			from = minute.Add(-maxCatchUp)
		}
		per := map[time.Time]int{}
		for at := from.Add(time.Minute); !at.After(minute); at = at.Add(time.Minute) {
			per[hourOf(at)]++
		}
		for hour, count := range per {
			res, err := tx.Exec(ctx, `INSERT INTO loomplane_platform_meter_compute (placement_id,generation,hour,org_id,vcpus,memory_mib,minutes,last_minute)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
ON CONFLICT (placement_id,generation,hour) DO UPDATE SET minutes=loomplane_platform_meter_compute.minutes+EXCLUDED.minutes,
last_minute=GREATEST(loomplane_platform_meter_compute.last_minute,EXCLUDED.last_minute)
WHERE NOT loomplane_platform_meter_compute.emitted`,
				p.ID, p.Generation, hour, p.OrgID, p.VCPUs, p.MemoryMiB, count, minute)
			if err != nil {
				return err
			}
			if rows, rErr := res.RowsAffected(); rErr == nil && rows == 0 {
				slog.WarnContext(ctx, "platform meter: minutes for an hour already emitted", "placement", p.ID, "hour", hour)
			}
		}
		return nil
	})
}

type computeHour struct {
	placementID string
	generation  int64
	hour        time.Time
	orgID       string
	vcpus, mib  int
	minutes     int
}

// emitComputeHours records the events of every closed hour not yet emitted.
// Each hour is emitted in one transaction that holds the placement's meter
// lock and the row, so a count can't add a minute between the read and the
// emitted flag. Record is idempotent on ID, so a crash between Record and the
// commit emits the same events again with the same IDs.
func (s *Service) emitComputeHours(ctx context.Context, now time.Time) error {
	cutoff := hourOf(now.Add(-emitDelay))
	rows, err := s.store.db.Query(ctx, `SELECT placement_id,generation,hour FROM loomplane_platform_meter_compute
WHERE NOT emitted AND hour<$1 ORDER BY hour,placement_id,generation`, cutoff)
	if err != nil {
		return err
	}
	var due []computeHour
	for rows.Next() {
		var h computeHour
		if err = rows.Scan(&h.placementID, &h.generation, &h.hour); err != nil {
			_ = rows.Close()
			return err
		}
		h.hour = h.hour.UTC()
		due = append(due, h)
	}
	if err = rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	_ = rows.Close()

	var errs []error
	for _, h := range due {
		if err = s.emitComputeHour(ctx, h); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (s *Service) emitComputeHour(ctx context.Context, h computeHour) error {
	return s.store.withTx(ctx, func(tx driverTx) error {
		if err := lockMeterRow(ctx, tx, "compute:"+h.placementID); err != nil {
			return err
		}
		err := tx.QueryRow(ctx, `SELECT org_id,vcpus,memory_mib,minutes FROM loomplane_platform_meter_compute
WHERE placement_id=$1 AND generation=$2 AND hour=$3 AND NOT emitted FOR UPDATE`, h.placementID, h.generation, h.hour).Scan(&h.orgID, &h.vcpus, &h.mib, &h.minutes)
		if err != nil {
			if errors.Is(storeError(err), ErrNotFound) {
				return nil // another sampler emitted it
			}
			return err
		}
		hours := float64(h.minutes) / 60
		base := fmt.Sprintf("platform:%s:%d:%s:", h.placementID, h.generation, h.hour.Format(time.RFC3339))
		for _, e := range []metering.Event{
			{ID: base + FeatureComputeVCPUHours, Feature: FeatureComputeVCPUHours, Quantity: float64(h.vcpus) * hours},
			{ID: base + FeatureComputeGBRAMHours, Feature: FeatureComputeGBRAMHours, Quantity: float64(h.mib) / 1024 * hours},
		} {
			e.OrgID, e.ResourceRef, e.OccurredAt, e.PlatformOwned = h.orgID, h.placementID, h.hour, true
			if err = s.meterOrDiscard().Record(ctx, e); err != nil {
				return fmt.Errorf("record %s: %w", e.ID, err)
			}
		}
		_, err = tx.Exec(ctx, `UPDATE loomplane_platform_meter_compute SET emitted=true WHERE placement_id=$1 AND generation=$2 AND hour=$3`,
			h.placementID, h.generation, h.hour)
		return err
	})
}

// bytesPerGB: egress is metered in decimal gigabytes (10^9 bytes), not GiB.
const bytesPerGB = 1e9

func (s *Service) usageOf(ctx context.Context, hostID string) ([]fc.UsageSample, error) {
	if s.usage != nil {
		return s.usage(ctx, hostID)
	}
	c, err := s.client(ctx, hostID)
	if err != nil {
		return nil, err
	}
	defer c.Close() //nolint:errcheck
	return c.Usage(ctx)
}

// SampleEgress reads every ready or draining host's byte counters, adds what
// each placement sent since the last read to its hour and emits the egress
// events of every hour that has closed.
//
// The last value read is kept per placement and counter ID in
// loomplane_platform_egress, so the delta survives a restart. A counter ID
// not seen before starts from 0 and counts in full. A counter that goes
// backwards under the same ID is a reset: the new value counts in full and
// the reset is logged. Samples whose scope maps to no placement are ignored
// and logged by VM ID only. The hourly event, platform-egress:<placement>:<hour>,
// carries the hour's total in decimal GB and is emitted once the hour is over.
//
// Reading a host and counting what it returned happen in one transaction that
// holds a per-host lock, so two samplers can't commit one host's readings out
// of order and mistake the older one for a reset.
//
// Bytes a placement's old VM sent while it moved count to the same
// placement: they are the organization's bytes. Bytes sent between the last
// sample and the VM's removal are not seen, at most one sampling interval.
func (s *Service) SampleEgress(ctx context.Context, now time.Time) error {
	hosts, err := s.store.ListHosts(ctx, "")
	if err != nil {
		return err
	}
	var errs []error
	for _, h := range hosts {
		if h.Status != "ready" && h.Status != "draining" {
			continue
		}
		if hErr := s.countHostEgress(ctx, h.ID, now); hErr != nil {
			slog.ErrorContext(ctx, "platform meter: sampling host egress failed", "host", h.ID, "error", hErr)
			errs = append(errs, fmt.Errorf("sample egress of host %s: %w", h.ID, hErr))
		}
	}
	errs = append(errs, s.emitEgressHours(ctx, now))
	return errors.Join(errs...)
}

func (s *Service) countHostEgress(ctx context.Context, hostID string, now time.Time) error {
	return s.store.withTx(ctx, func(tx driverTx) error {
		if err := lockMeterRow(ctx, tx, "egress-host:"+hostID); err != nil {
			return err
		}
		samples, err := s.usageOf(ctx, hostID)
		if err != nil {
			return err
		}
		for _, smp := range samples {
			if err = s.countEgress(ctx, tx, smp, now); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Service) countEgress(ctx context.Context, tx driverTx, smp fc.UsageSample, now time.Time) error {
	if smp.Scope.Connection != fc.PlatformConnection {
		slog.WarnContext(ctx, "platform meter: usage sample outside the platform connection", "vm", smp.VMID)
		return nil
	}
	p, err := scanPlacement(tx.QueryRow(ctx, `SELECT `+placementColumns+` FROM loomplane_platform_placements
WHERE org_id=$1 AND instance_id=$2 AND instance_id<>'' AND state<>'released' ORDER BY created_at DESC LIMIT 1`, smp.Scope.Org, smp.Scope.Resource))
	if err != nil {
		if errors.Is(storeError(err), ErrNotFound) {
			slog.WarnContext(ctx, "platform meter: usage sample maps to no placement", "vm", smp.VMID)
			return nil
		}
		return err
	}
	if smp.Bytes > math.MaxInt64 {
		slog.WarnContext(ctx, "platform meter: usage sample out of range", "vm", smp.VMID)
		return nil
	}
	cur := int64(smp.Bytes)
	if err = lockMeterRow(ctx, tx, "egress:"+p.ID); err != nil {
		return err
	}
	var last int64
	if qErr := tx.QueryRow(ctx, `SELECT last_bytes FROM loomplane_platform_egress WHERE placement_id=$1 AND counter_id=$2`, p.ID, smp.CounterID).Scan(&last); qErr != nil {
		if !errors.Is(storeError(qErr), ErrNotFound) {
			return qErr
		}
		last = 0
	}
	delta := cur - last
	if delta < 0 {
		slog.WarnContext(ctx, "platform meter: egress counter went backwards, counting it as a reset", "vm", smp.VMID, "placement", p.ID)
		delta = cur
	}
	if _, eErr := tx.Exec(ctx, `INSERT INTO loomplane_platform_egress (placement_id,counter_id,last_bytes) VALUES ($1,$2,$3)
ON CONFLICT (placement_id,counter_id) DO UPDATE SET last_bytes=EXCLUDED.last_bytes`, p.ID, smp.CounterID, cur); eErr != nil {
		return eErr
	}
	if delta == 0 {
		return nil
	}
	res, eErr := tx.Exec(ctx, `INSERT INTO loomplane_platform_meter_egress (placement_id,hour,org_id,bytes) VALUES ($1,$2,$3,$4)
ON CONFLICT (placement_id,hour) DO UPDATE SET bytes=loomplane_platform_meter_egress.bytes+EXCLUDED.bytes
WHERE NOT loomplane_platform_meter_egress.emitted`, p.ID, hourOf(now), p.OrgID, delta)
	if eErr != nil {
		return eErr
	}
	if n, rErr := res.RowsAffected(); rErr == nil && n == 0 {
		slog.WarnContext(ctx, "platform meter: egress for an hour already emitted", "placement", p.ID)
	}
	return nil
}

// emitEgressHours records every closed egress hour not yet emitted, each in
// one transaction holding the placement's meter lock and the row.
func (s *Service) emitEgressHours(ctx context.Context, now time.Time) error {
	rows, err := s.store.db.Query(ctx, `SELECT placement_id,hour FROM loomplane_platform_meter_egress
WHERE NOT emitted AND hour<$1 ORDER BY hour,placement_id`, hourOf(now.Add(-emitDelay)))
	if err != nil {
		return err
	}
	type due struct {
		placementID string
		hour        time.Time
	}
	var all []due
	for rows.Next() {
		var d due
		if err = rows.Scan(&d.placementID, &d.hour); err != nil {
			_ = rows.Close()
			return err
		}
		d.hour = d.hour.UTC()
		all = append(all, d)
	}
	if err = rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	_ = rows.Close()

	var errs []error
	for _, d := range all {
		if err = s.emitEgressHour(ctx, d.placementID, d.hour); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (s *Service) emitEgressHour(ctx context.Context, placementID string, hour time.Time) error {
	return s.store.withTx(ctx, func(tx driverTx) error {
		if err := lockMeterRow(ctx, tx, "egress:"+placementID); err != nil {
			return err
		}
		var orgID string
		var bytes int64
		if err := tx.QueryRow(ctx, `SELECT org_id,bytes FROM loomplane_platform_meter_egress WHERE placement_id=$1 AND hour=$2 AND NOT emitted FOR UPDATE`,
			placementID, hour).Scan(&orgID, &bytes); err != nil {
			if errors.Is(storeError(err), ErrNotFound) {
				return nil
			}
			return err
		}
		e := metering.Event{ID: "platform-egress:" + placementID + ":" + hour.Format(time.RFC3339), OrgID: orgID, Feature: FeatureEgressGB,
			ResourceRef: placementID, Quantity: float64(bytes) / bytesPerGB, OccurredAt: hour, PlatformOwned: true}
		if err := s.meterOrDiscard().Record(ctx, e); err != nil {
			return fmt.Errorf("record %s: %w", e.ID, err)
		}
		_, err := tx.Exec(ctx, `UPDATE loomplane_platform_meter_egress SET emitted=true WHERE placement_id=$1 AND hour=$2`, placementID, hour)
		return err
	})
}

// RecordBuild meters a build VM that ran from started to ended as whole
// minutes, rounded up. The event ID is the build placement's, so recording
// the same build twice counts it once. A build that took no time records
// nothing.
func (s *Service) RecordBuild(ctx context.Context, p Placement, started, ended time.Time) error {
	d := ended.Sub(started)
	if d <= 0 {
		return nil
	}
	return s.meterOrDiscard().Record(ctx, metering.Event{
		ID: "platform-build:" + p.ID, OrgID: p.OrgID, Feature: FeatureBuildMinutes, ResourceRef: p.ID,
		Quantity: float64((d + time.Minute - 1) / time.Minute), OccurredAt: ended.UTC(), PlatformOwned: true,
	})
}
