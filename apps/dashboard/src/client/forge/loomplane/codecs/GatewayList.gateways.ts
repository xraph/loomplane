import type { Codec } from '../codec-runtime';
import { codec_GatewayStatus } from './GatewayStatus';

export const codec_GatewayList_gateways: Codec = {"kind": "array", "items": () => codec_GatewayStatus};
