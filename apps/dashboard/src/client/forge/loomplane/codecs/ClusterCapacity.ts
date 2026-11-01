import type { Codec } from '../codec-runtime';
import { codec_ClusterCapacity_conditions } from './ClusterCapacity.conditions';
import { codec_ClusterCapacity_nodes } from './ClusterCapacity.nodes';

export const codec_ClusterCapacity: Codec = {"kind": "object", "fields": {"checked_at": {"ts": "checkedAt"}, "complete": {"ts": "complete"}, "conditions": {"ts": "conditions", "codec": () => codec_ClusterCapacity_conditions}, "fresh": {"ts": "fresh"}, "nodes": {"ts": "nodes", "codec": () => codec_ClusterCapacity_nodes}, "observation_revision": {"ts": "observationRevision"}, "resource_id": {"ts": "resourceId"}, "workloads_complete": {"ts": "workloadsComplete"}}, "required": ["checked_at", "complete", "conditions", "fresh", "nodes", "observation_revision", "resource_id", "workloads_complete"]};
