import type { Codec } from '../codec-runtime';
import { codec_CapacityResources } from './CapacityResources';
import { codec_CloudinfraClusterNodeCapacity_gpu_kinds } from './CloudinfraClusterNodeCapacity.gpu_kinds';
import { codec_CloudinfraClusterNodeCapacity_provenance } from './CloudinfraClusterNodeCapacity.provenance';
import { codec_CloudinfraClusterNodeCapacity_taints } from './CloudinfraClusterNodeCapacity.taints';
import { codec_CloudinfraClusterResourceMetrics } from './CloudinfraClusterResourceMetrics';

export const codec_CloudinfraClusterNodeCapacity: Codec = {"kind": "object", "fields": {"allocatable": {"ts": "allocatable", "codec": () => codec_CloudinfraClusterResourceMetrics}, "architecture": {"ts": "architecture"}, "external_vm_id": {"ts": "externalVmId"}, "gpu_kinds": {"ts": "gpuKinds", "codec": () => codec_CloudinfraClusterNodeCapacity_gpu_kinds}, "node_uid": {"ts": "nodeUid"}, "pool_id": {"ts": "poolId"}, "provenance": {"ts": "provenance", "codec": () => codec_CloudinfraClusterNodeCapacity_provenance}, "provisioned": {"ts": "provisioned", "codec": () => codec_CapacityResources}, "ready": {"ts": "ready"}, "requests": {"ts": "requests", "codec": () => codec_CloudinfraClusterResourceMetrics}, "taints": {"ts": "taints", "codec": () => codec_CloudinfraClusterNodeCapacity_taints}, "unschedulable": {"ts": "unschedulable"}, "zone": {"ts": "zone"}}, "required": ["allocatable", "architecture", "external_vm_id", "gpu_kinds", "node_uid", "pool_id", "provenance", "provisioned", "ready", "requests", "taints", "unschedulable", "zone"]};
