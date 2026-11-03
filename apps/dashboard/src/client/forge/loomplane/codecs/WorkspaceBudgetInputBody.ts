import type { Codec } from '../codec-runtime';

export const codec_WorkspaceBudgetInputBody: Codec = {"kind": "object", "fields": {"monthly_budget_usd": {"ts": "monthlyBudgetUsd"}, "revision": {"ts": "revision"}}, "required": ["monthly_budget_usd", "revision"]};
