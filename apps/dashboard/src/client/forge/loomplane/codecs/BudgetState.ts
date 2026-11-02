import type { Codec } from '../codec-runtime';

export const codec_BudgetState: Codec = {"kind": "object", "fields": {"exhausted": {"ts": "exhausted"}, "limit_usd": {"ts": "limitUsd"}, "spent_usd": {"ts": "spentUsd"}}, "required": ["exhausted", "limit_usd", "spent_usd"]};
