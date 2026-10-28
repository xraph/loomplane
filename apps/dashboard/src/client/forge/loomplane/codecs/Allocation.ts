import type { Codec } from '../codec-runtime';

export const codec_Allocation: Codec = {"kind": "object", "fields": {"environment_id": {"ts": "environmentId"}, "label": {"ts": "label"}, "monthly_budget_usd": {"ts": "monthlyBudgetUsd"}, "project_id": {"ts": "projectId"}, "scope": {"ts": "scope"}, "updated_at": {"ts": "updatedAt"}}, "required": ["label", "monthly_budget_usd", "scope", "updated_at"]};
