import type { Codec } from '../codec-runtime';

export const codec_ClusterNativeWorkload: Codec = {"kind": "object", "fields": {"maximum_nodes": {"ts": "maximumNodes"}, "policy_id": {"ts": "policyId"}, "policy_revision": {"ts": "policyRevision"}, "pool_id": {"ts": "poolId"}, "profile_digest": {"ts": "profileDigest"}}, "required": ["maximum_nodes", "policy_id", "policy_revision", "pool_id", "profile_digest"]};
