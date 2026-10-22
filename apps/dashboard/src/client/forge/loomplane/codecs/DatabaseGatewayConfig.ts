import type { Codec } from '../codec-runtime';
import { codec_DatabaseGatewayConfig_resources } from './DatabaseGatewayConfig.resources';

export const codec_DatabaseGatewayConfig: Codec = {"kind": "object", "fields": {"name": {"ts": "name"}, "program": {"ts": "program"}, "resources": {"ts": "resources", "codec": () => codec_DatabaseGatewayConfig_resources}, "suffix": {"ts": "suffix"}}, "required": ["name", "program", "resources", "suffix"]};
