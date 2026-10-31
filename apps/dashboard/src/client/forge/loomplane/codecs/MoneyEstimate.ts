import type { Codec } from '../codec-runtime';
import { codec_MoneyEstimate_components } from './MoneyEstimate.components';
import { codec_MoneyEstimate_missing_components } from './MoneyEstimate.missing_components';

export const codec_MoneyEstimate: Codec = {"kind": "object", "fields": {"checked_at": {"ts": "checkedAt"}, "components": {"ts": "components", "codec": () => codec_MoneyEstimate_components}, "horizon_micro_usd": {"ts": "horizonMicroUsd"}, "horizon_seconds": {"ts": "horizonSeconds"}, "hourly_micro_usd": {"ts": "hourlyMicroUsd"}, "known": {"ts": "known"}, "missing_components": {"ts": "missingComponents", "codec": () => codec_MoneyEstimate_missing_components}, "price_digest": {"ts": "priceDigest"}}, "required": ["checked_at", "components", "horizon_micro_usd", "horizon_seconds", "hourly_micro_usd", "known", "missing_components", "price_digest"]};
