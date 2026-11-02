import type { Codec } from '../codec-runtime';
import { codec_ClusterMaintenance } from './ClusterMaintenance';
import { codec_ClusterObservation_conditions } from './ClusterObservation.conditions';
import { codec_ClusterObservation_pools } from './ClusterObservation.pools';
import { codec_ClusterRef } from './ClusterRef';

export const codec_ClusterObservation: Codec = {"kind": "object", "fields": {"ca_hash": {"ts": "caHash"}, "conditions": {"ts": "conditions", "codec": () => codec_ClusterObservation_conditions}, "endpoint": {"ts": "endpoint"}, "ha": {"ts": "ha"}, "maintenance": {"ts": "maintenance", "codec": () => codec_ClusterMaintenance}, "name": {"ts": "name"}, "observed_at": {"ts": "observedAt"}, "pools": {"ts": "pools", "codec": () => codec_ClusterObservation_pools}, "ref": {"ts": "ref", "codec": () => codec_ClusterRef}, "state": {"ts": "state"}, "version": {"ts": "version"}}, "required": ["ca_hash", "conditions", "endpoint", "name", "observed_at", "pools", "ref", "state", "version"]};
