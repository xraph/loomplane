import type { Codec } from '../codec-runtime';

export const codec_RouteCost: Codec = {"kind": "object", "fields": {"estimate": {"ts": "estimate"}, "monthly_usd": {"ts": "monthlyUsd"}, "source": {"ts": "source"}}, "required": ["estimate", "monthly_usd", "source"]};
