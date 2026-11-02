import type { Codec } from '../codec-runtime';
import { codec_ClusterMaintenanceWindow } from './ClusterMaintenanceWindow';
import { codec_ClusterUpdate } from './ClusterUpdate';
import { codec_NodePoolSpec } from './NodePoolSpec';

export const codec_ClusterMaintenanceInput: Codec = {"kind": "object", "fields": {"drain_digest": {"ts": "drainDigest"}, "observation_digest": {"ts": "observationDigest"}, "observed_revision": {"ts": "observedRevision"}, "pool": {"ts": "pool", "codec": () => codec_NodePoolSpec}, "pool_id": {"ts": "poolId"}, "update": {"ts": "update", "codec": () => codec_ClusterUpdate}, "version": {"ts": "version"}, "window": {"ts": "window", "codec": () => codec_ClusterMaintenanceWindow}}, "required": ["observation_digest", "observed_revision"]};
