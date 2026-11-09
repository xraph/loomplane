import type { Codec } from '../codec-runtime';
import { codec_ClientClusterNodeCapacity_taints } from './ClientClusterNodeCapacity.taints';
import { codec_ClientClusterResourceMetrics } from './ClientClusterResourceMetrics';

export const codec_ClientClusterNodeCapacity: Codec = {"kind": "object", "fields": {"allocatable": {"ts": "allocatable", "codec": () => codec_ClientClusterResourceMetrics}, "architecture": {"ts": "architecture"}, "pool_id": {"ts": "poolId"}, "ready": {"ts": "ready"}, "requests": {"ts": "requests", "codec": () => codec_ClientClusterResourceMetrics}, "taints": {"ts": "taints", "codec": () => codec_ClientClusterNodeCapacity_taints}, "unschedulable": {"ts": "unschedulable"}, "zone": {"ts": "zone"}}, "required": ["allocatable", "architecture", "pool_id", "ready", "requests", "taints", "unschedulable", "zone"]};
