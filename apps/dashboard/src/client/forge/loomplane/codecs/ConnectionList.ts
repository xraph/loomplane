import type { Codec } from '../codec-runtime';
import { codec_ConnectionList_connections } from './ConnectionList.connections';

export const codec_ConnectionList: Codec = {"kind": "object", "fields": {"connections": {"ts": "connections", "codec": () => codec_ConnectionList_connections}}, "required": ["connections"]};
