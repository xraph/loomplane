import type { Codec } from '../codec-runtime';
import { codec_CloudCluster_lifecycle_authority } from './CloudCluster.lifecycle_authority';
import { codec_ClusterObservation } from './ClusterObservation';

export const codec_CloudCluster: Codec = {"kind": "object", "fields": {"account_id": {"ts": "accountId"}, "cloud_id": {"ts": "cloudId"}, "connection_id": {"ts": "connectionId"}, "external_id": {"ts": "externalId"}, "id": {"ts": "id"}, "lifecycle_authority": {"ts": "lifecycleAuthority", "codec": () => codec_CloudCluster_lifecycle_authority}, "observation": {"ts": "observation", "codec": () => codec_ClusterObservation}, "ownership": {"ts": "ownership"}, "provider": {"ts": "provider"}, "revision": {"ts": "revision"}, "status": {"ts": "status"}, "updated_at": {"ts": "updatedAt"}}, "required": ["account_id", "cloud_id", "connection_id", "external_id", "id", "lifecycle_authority", "ownership", "provider", "revision", "status", "updated_at"]};
