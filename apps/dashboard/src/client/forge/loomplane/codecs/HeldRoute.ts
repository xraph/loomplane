import type { Codec } from '../codec-runtime';
import { codec_HeldRoute_route_ids } from './HeldRoute.route_ids';

export const codec_HeldRoute: Codec = {"kind": "object", "fields": {"key": {"ts": "key"}, "route_ids": {"ts": "routeIds", "codec": () => codec_HeldRoute_route_ids}, "service": {"ts": "service"}}, "required": ["key", "route_ids", "service"]};
