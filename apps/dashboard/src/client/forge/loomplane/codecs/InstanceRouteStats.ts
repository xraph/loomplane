import type { Codec } from '../codec-runtime';

export const codec_InstanceRouteStats: Codec = {"kind": "object", "fields": {"backend_conns": {"ts": "backendConns"}, "max_connections": {"ts": "maxConnections"}, "pin_rate": {"ts": "pinRate"}, "pinned": {"ts": "pinned"}, "reachable": {"ts": "reachable"}, "route_id": {"ts": "routeId"}, "wait_p95_ms": {"ts": "waitP95Ms"}}, "required": ["backend_conns", "max_connections", "pin_rate", "pinned", "reachable", "route_id", "wait_p95_ms"]};
