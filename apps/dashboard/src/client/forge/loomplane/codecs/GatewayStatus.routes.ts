import type { Codec } from '../codec-runtime';
import { codec_RouteStatus } from './RouteStatus';

export const codec_GatewayStatus_routes: Codec = {"kind": "array", "items": () => codec_RouteStatus};
