import type { Codec } from '../codec-runtime';

export const codec_RouteStatus: Codec = {"kind": "object", "fields": {"backend_conns": {"ts": "backendConns"}, "id": {"ts": "id"}, "max_connections": {"ts": "maxConnections"}, "pin_rate": {"ts": "pinRate"}, "reachable": {"ts": "reachable"}, "reported": {"ts": "reported"}, "resource": {"ts": "resource"}, "state": {"ts": "state"}}, "required": ["backend_conns", "id", "max_connections", "pin_rate", "reachable", "reported", "resource", "state"]};
