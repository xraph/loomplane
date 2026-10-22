import type { Codec } from '../codec-runtime';
import { codec_GatewayRoute } from './GatewayRoute';

export const codec_ServiceConfig_gateway: Codec = {"kind": "array", "items": () => codec_GatewayRoute};
