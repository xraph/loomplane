import type { Codec } from '../codec-runtime';
import { codec_CapacityObservation } from './CapacityObservation';
import { codec_CapacityRecord_cluster_conditions } from './CapacityRecord.cluster_conditions';
import { codec_CapacityResources } from './CapacityResources';
import { codec_ClusterNodeCapacity } from './ClusterNodeCapacity';

export const codec_CapacityRecord: Codec = {"kind": "object", "fields": {"account_id": {"ts": "accountId"}, "checked_at": {"ts": "checkedAt"}, "cluster_checked_at": {"ts": "clusterCheckedAt"}, "cluster_complete": {"ts": "clusterComplete"}, "cluster_conditions": {"ts": "clusterConditions", "codec": () => codec_CapacityRecord_cluster_conditions}, "cluster_node": {"ts": "clusterNode", "codec": () => codec_ClusterNodeCapacity}, "cluster_resource_id": {"ts": "clusterResourceId"}, "gpu_kind": {"ts": "gpuKind"}, "gpus": {"ts": "gpus"}, "kind": {"ts": "kind"}, "observation": {"ts": "observation", "codec": () => codec_CapacityObservation}, "provider": {"ts": "provider"}, "resource_id": {"ts": "resourceId"}, "resources": {"ts": "resources", "codec": () => codec_CapacityResources}, "server_id": {"ts": "serverId"}, "server_state_revision": {"ts": "serverStateRevision"}}, "required": ["account_id", "checked_at", "kind", "provider", "resource_id", "resources", "server_state_revision"]};
