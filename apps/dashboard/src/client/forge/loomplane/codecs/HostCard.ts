import type { Codec } from '../codec-runtime';
import { codec_CloudinfraRouteStatus } from './CloudinfraRouteStatus';
import { codec_HostCard_routes } from './HostCard.routes';

export const codec_HostCard: Codec = {"kind": "object", "fields": {"attached": {"ts": "attached"}, "domain_route_id": {"ts": "domainRouteId"}, "farp_home": {"ts": "farpHome"}, "hostname": {"ts": "hostname"}, "routes": {"ts": "routes", "codec": () => codec_HostCard_routes}, "status": {"ts": "status", "codec": () => codec_CloudinfraRouteStatus}}, "required": ["attached", "farp_home", "hostname", "routes"]};
