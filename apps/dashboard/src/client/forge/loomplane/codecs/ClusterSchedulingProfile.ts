import type { Codec } from '../codec-runtime';
import { codec_ClusterWorkloadVector } from './ClusterWorkloadVector';

export const codec_ClusterSchedulingProfile: Codec = {"kind": "object", "fields": {"allocatable": {"ts": "allocatable", "codec": () => codec_ClusterWorkloadVector}, "architecture": {"ts": "architecture"}, "available": {"ts": "available", "codec": () => codec_ClusterWorkloadVector}, "digest": {"ts": "digest"}, "overhead": {"ts": "overhead", "codec": () => codec_ClusterWorkloadVector}, "pool_id": {"ts": "poolId"}, "template_digest": {"ts": "templateDigest"}, "zone": {"ts": "zone"}}, "required": ["allocatable", "architecture", "available", "digest", "overhead", "pool_id", "template_digest", "zone"]};
