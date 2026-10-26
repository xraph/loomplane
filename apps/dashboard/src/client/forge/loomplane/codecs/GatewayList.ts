import type { Codec } from '../codec-runtime';
import { codec_GatewayList_gateways } from './GatewayList.gateways';

export const codec_GatewayList: Codec = {"kind": "object", "fields": {"gateways": {"ts": "gateways", "codec": () => codec_GatewayList_gateways}}, "required": ["gateways"]};
