import type { Codec } from '../codec-runtime';
import { codec_DomainRouteList_routes } from './DomainRouteList.routes';

export const codec_DomainRouteList: Codec = {"kind": "object", "fields": {"routes": {"ts": "routes", "codec": () => codec_DomainRouteList_routes}}, "required": ["routes"]};
