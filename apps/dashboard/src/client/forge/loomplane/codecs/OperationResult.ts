import type { Codec } from '../codec-runtime';
import { codec_FleetOperation } from './FleetOperation';
import { codec_Instance } from './Instance';

export const codec_OperationResult: Codec = {"kind": "object", "fields": {"instance": {"ts": "instance", "codec": () => codec_Instance}, "operation": {"ts": "operation", "codec": () => codec_FleetOperation}}, "required": ["instance", "operation"]};
