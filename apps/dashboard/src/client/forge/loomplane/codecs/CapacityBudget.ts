import type { Codec } from '../codec-runtime';

export const codec_CapacityBudget: Codec = {"kind": "object", "fields": {"horizon_seconds": {"ts": "horizonSeconds"}, "max_horizon_micro_usd": {"ts": "maxHorizonMicroUsd"}, "max_hourly_micro_usd": {"ts": "maxHourlyMicroUsd"}}, "required": ["horizon_seconds", "max_horizon_micro_usd", "max_hourly_micro_usd"]};
