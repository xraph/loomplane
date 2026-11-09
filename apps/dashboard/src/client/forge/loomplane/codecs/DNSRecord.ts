import type { Codec } from '../codec-runtime';

export const codec_DNSRecord: Codec = {"kind": "object", "fields": {"id": {"ts": "id"}, "name": {"ts": "name"}, "owner": {"ts": "owner"}, "proxied": {"ts": "proxied"}, "ttl": {"ts": "ttl"}, "type": {"ts": "type"}, "value": {"ts": "value"}, "zone_id": {"ts": "zoneId"}}, "required": ["id", "name", "ttl", "type", "value", "zone_id"]};
