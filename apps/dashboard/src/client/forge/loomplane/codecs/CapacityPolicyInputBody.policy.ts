import type { Codec } from '../codec-runtime';
import { codec_CapacityBudget } from './CapacityBudget';
import { codec_CapacityPolicyInputBody_policy_frontend_limits } from './CapacityPolicyInputBody.policy.frontend_limits';
import { codec_CapacityPolicyInputBody_policy_gpu_limits } from './CapacityPolicyInputBody.policy.gpu_limits';
import { codec_CapacityPolicyInputBody_policy_pools } from './CapacityPolicyInputBody.policy.pools';
import { codec_ResourceVector } from './ResourceVector';

export const codec_CapacityPolicyInputBody_policy: Codec = {"kind": "object", "fields": {"budget": {"ts": "budget", "codec": () => codec_CapacityBudget}, "cloud_id": {"ts": "cloudId"}, "enabled": {"ts": "enabled"}, "frontend_limits": {"ts": "frontendLimits", "codec": () => codec_CapacityPolicyInputBody_policy_frontend_limits}, "gpu_limits": {"ts": "gpuLimits", "codec": () => codec_CapacityPolicyInputBody_policy_gpu_limits}, "limits": {"ts": "limits", "codec": () => codec_ResourceVector}, "org_id": {"ts": "orgId"}, "pools": {"ts": "pools", "codec": () => codec_CapacityPolicyInputBody_policy_pools}, "require_expansion_approval": {"ts": "requireExpansionApproval"}, "revision": {"ts": "revision"}}, "required": ["cloud_id", "enabled", "frontend_limits", "limits", "org_id", "pools", "require_expansion_approval", "revision"]};
