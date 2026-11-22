import type { Codec } from '../codec-runtime';
import { codec_EngineCapacity } from './EngineCapacity';

export const codec_DatabaseCapacity: Codec = {"kind": "object", "fields": {"mysql": {"ts": "mysql", "codec": () => codec_EngineCapacity}, "pg": {"ts": "pg", "codec": () => codec_EngineCapacity}, "valkey": {"ts": "valkey", "codec": () => codec_EngineCapacity}}, "required": ["mysql", "pg", "valkey"]};
