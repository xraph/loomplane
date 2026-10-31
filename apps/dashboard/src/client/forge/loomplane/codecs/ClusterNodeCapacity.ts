import type { Codec } from '../codec-runtime';
import { codec_CapacityResources } from './CapacityResources';
import { codec_ClusterNodeCapacity_gpu_kinds } from './ClusterNodeCapacity.gpu_kinds';
import { codec_ClusterNodeCapacity_provenance } from './ClusterNodeCapacity.provenance';
import { codec_ClusterNodeCapacity_taints } from './ClusterNodeCapacity.taints';
import { codec_ClusterResourceMetrics } from './ClusterResourceMetrics';

export const codec_ClusterNodeCapacity: Codec = {"kind": "object", "fields": {"allocatable": {"ts": "allocatable", "codec": () => codec_ClusterResourceMetrics}, "architecture": {"ts": "architecture"}, "external_vm_id": {"ts": "externalVmId"}, "gpu_kinds": {"ts": "gpuKinds", "codec": () => codec_ClusterNodeCapacity_gpu_kinds}, "node_uid": {"ts": "nodeUid"}, "pool_id": {"ts": "poolId"}, "provenance": {"ts": "provenance", "codec": () => codec_ClusterNodeCapacity_provenance}, "provisioned": {"ts": "provisioned", "codec": () => codec_CapacityResources}, "ready": {"ts": "ready"}, "requests": {"ts": "requests", "codec": () => codec_ClusterResourceMetrics}, "taints": {"ts": "taints", "codec": () => codec_ClusterNodeCapacity_taints}, "unschedulable": {"ts": "unschedulable"}, "zone": {"ts": "zone"}}, "required": ["allocatable", "architecture", "external_vm_id", "gpu_kinds", "node_uid", "pool_id", "provenance", "provisioned", "ready", "requests", "taints", "unschedulable", "zone"]};
