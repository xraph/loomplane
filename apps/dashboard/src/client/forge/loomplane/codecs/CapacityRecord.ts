import type { Codec } from '../codec-runtime';
import { codec_CapacityObservation } from './CapacityObservation';
import { codec_CapacityResources } from './CapacityResources';

export const codec_CapacityRecord: Codec = {"kind": "object", "fields": {"account_id": {"ts": "accountId"}, "checked_at": {"ts": "checkedAt"}, "gpu_kind": {"ts": "gpuKind"}, "gpus": {"ts": "gpus"}, "kind": {"ts": "kind"}, "observation": {"ts": "observation", "codec": () => codec_CapacityObservation}, "provider": {"ts": "provider"}, "resource_id": {"ts": "resourceId"}, "resources": {"ts": "resources", "codec": () => codec_CapacityResources}, "server_id": {"ts": "serverId"}, "server_state_revision": {"ts": "serverStateRevision"}}, "required": ["account_id", "checked_at", "kind", "provider", "resource_id", "resources", "server_state_revision"]};
