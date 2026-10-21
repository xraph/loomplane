import type { Codec } from '../codec-runtime';

export const codec_RouteInput: Codec = {"kind": "object", "fields": {"cloud_id": {"ts": "cloudId"}, "environment_id": {"ts": "environmentId"}, "hostname": {"ts": "hostname"}, "port": {"ts": "port"}, "service": {"ts": "service"}, "zone_id": {"ts": "zoneId"}}, "required": ["cloud_id", "environment_id", "hostname", "port", "service", "zone_id"]};
