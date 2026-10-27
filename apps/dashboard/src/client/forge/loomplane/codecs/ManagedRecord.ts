import type { Codec } from '../codec-runtime';

export const codec_ManagedRecord: Codec = {"kind": "object", "fields": {"hostname": {"ts": "hostname"}, "kind": {"ts": "kind"}, "name": {"ts": "name"}, "project_id": {"ts": "projectId"}, "project_name": {"ts": "projectName"}, "route_id": {"ts": "routeId"}, "ttl": {"ts": "ttl"}, "type": {"ts": "type"}, "value": {"ts": "value"}, "zone_id": {"ts": "zoneId"}}, "required": ["kind", "name", "ttl", "type", "value", "zone_id"]};
