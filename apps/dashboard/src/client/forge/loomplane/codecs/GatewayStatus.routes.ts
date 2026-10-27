import type { Codec } from '../codec-runtime';
import { codec_DbgatewayRouteStatus } from './DbgatewayRouteStatus';

export const codec_GatewayStatus_routes: Codec = {"kind": "array", "items": () => codec_DbgatewayRouteStatus};
