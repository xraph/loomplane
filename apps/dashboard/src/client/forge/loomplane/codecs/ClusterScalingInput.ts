import type { Codec } from '../codec-runtime';

export const codec_ClusterScalingInput: Codec = {"kind": "object", "fields": {"continuity_floor": {"ts": "continuityFloor"}, "continuity_pool_id": {"ts": "continuityPoolId"}, "cooldown_seconds": {"ts": "cooldownSeconds"}, "expected_revision": {"ts": "expectedRevision"}, "max_nodes": {"ts": "maxNodes"}, "min_nodes": {"ts": "minNodes"}, "mode": {"ts": "mode"}, "pool_id": {"ts": "poolId"}}, "required": ["continuity_floor", "cooldown_seconds", "expected_revision", "max_nodes", "min_nodes", "mode", "pool_id"]};
