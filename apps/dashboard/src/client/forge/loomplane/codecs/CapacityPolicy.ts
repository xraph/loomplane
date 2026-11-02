import type { Codec } from '../codec-runtime';
import { codec_CapacityBudget } from './CapacityBudget';
import { codec_CapacityPolicy_frontend_limits } from './CapacityPolicy.frontend_limits';
import { codec_CapacityPolicy_gpu_limits } from './CapacityPolicy.gpu_limits';
import { codec_CapacityPolicy_pools } from './CapacityPolicy.pools';
import { codec_ResourceVector } from './ResourceVector';

export const codec_CapacityPolicy: Codec = {"kind": "object", "fields": {"budget": {"ts": "budget", "codec": () => codec_CapacityBudget}, "cloud_id": {"ts": "cloudId"}, "enabled": {"ts": "enabled"}, "frontend_limits": {"ts": "frontendLimits", "codec": () => codec_CapacityPolicy_frontend_limits}, "gpu_limits": {"ts": "gpuLimits", "codec": () => codec_CapacityPolicy_gpu_limits}, "limits": {"ts": "limits", "codec": () => codec_ResourceVector}, "org_id": {"ts": "orgId"}, "pools": {"ts": "pools", "codec": () => codec_CapacityPolicy_pools}, "require_expansion_approval": {"ts": "requireExpansionApproval"}, "revision": {"ts": "revision"}}, "required": ["cloud_id", "enabled", "frontend_limits", "limits", "org_id", "pools", "require_expansion_approval", "revision"]};
