import type { Codec } from '../codec-runtime';
import { codec_InstanceStatus_routes } from './InstanceStatus.routes';

export const codec_InstanceStatus: Codec = {"kind": "object", "fields": {"behind": {"ts": "behind"}, "draining": {"ts": "draining"}, "id": {"ts": "id"}, "last_seen_at": {"ts": "lastSeenAt"}, "reporting": {"ts": "reporting"}, "routes": {"ts": "routes", "codec": () => codec_InstanceStatus_routes}, "snapshot_version": {"ts": "snapshotVersion"}}, "required": ["behind", "draining", "id", "last_seen_at", "reporting", "routes", "snapshot_version"]};
