import type { Codec } from '../codec-runtime';
import { codec_RouteInputBody_blocked_path_prefixes } from './RouteInputBody.blocked_path_prefixes';

export const codec_RouteInputBody: Codec = {"kind": "object", "fields": {"blocked_path_prefixes": {"ts": "blockedPathPrefixes", "codec": () => codec_RouteInputBody_blocked_path_prefixes}, "cloud_id": {"ts": "cloudId"}, "environment_id": {"ts": "environmentId"}, "external_ttl_seconds": {"ts": "externalTtlSeconds"}, "hostname": {"ts": "hostname"}, "port": {"ts": "port"}, "service": {"ts": "service"}, "zone_id": {"ts": "zoneId"}}, "required": ["cloud_id", "environment_id", "hostname", "port", "service", "zone_id"]};
