import type { Codec } from '../codec-runtime';
import { codec_MoneyEstimate } from './MoneyEstimate';

export const codec_BudgetReview: Codec = {"kind": "object", "fields": {"cloud_id": {"ts": "cloudId"}, "digest": {"ts": "digest"}, "estimate": {"ts": "estimate", "codec": () => codec_MoneyEstimate}, "policy_revision": {"ts": "policyRevision"}}, "required": ["cloud_id", "digest", "estimate", "policy_revision"]};
