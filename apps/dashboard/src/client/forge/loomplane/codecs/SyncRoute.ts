import type { Codec } from '../codec-runtime';

export const codec_SyncRoute: Codec = {"kind": "object", "fields": {"cutover_id": {"ts": "cutoverId"}, "hostname": {"ts": "hostname"}, "message": {"ts": "message"}, "not_before": {"ts": "notBefore"}, "prepare_id": {"ts": "prepareId"}, "record_type": {"ts": "recordType"}, "release_id": {"ts": "releaseId"}, "route_id": {"ts": "routeId"}, "status": {"ts": "status"}, "ttl": {"ts": "ttl"}, "value": {"ts": "value"}}, "required": ["hostname", "not_before", "prepare_id", "route_id", "status"]};
