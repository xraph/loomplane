import type { Codec } from '../codec-runtime';

export const codec_Cost: Codec = {"kind": "object", "fields": {"estimate": {"ts": "estimate"}, "hourly_usd": {"ts": "hourlyUsd"}, "known": {"ts": "known"}, "monthly_usd": {"ts": "monthlyUsd"}, "share": {"ts": "share"}, "source": {"ts": "source"}}, "required": ["estimate", "hourly_usd", "known", "monthly_usd", "share", "source"]};
