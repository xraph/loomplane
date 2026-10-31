import type { Codec } from '../codec-runtime';
import { codec_CapacityAccountSnapshot } from './CapacityAccountSnapshot';
import { codec_MoneyEstimate } from './MoneyEstimate';

export const codec_BudgetReview: Codec = {"kind": "object", "fields": {"account_exposure": {"ts": "accountExposure", "codec": () => codec_CapacityAccountSnapshot}, "account_scope": {"ts": "accountScope"}, "cloud_id": {"ts": "cloudId"}, "digest": {"ts": "digest"}, "estimate": {"ts": "estimate", "codec": () => codec_MoneyEstimate}, "policy_revision": {"ts": "policyRevision"}}, "required": ["account_scope", "cloud_id", "digest", "estimate", "policy_revision"]};
