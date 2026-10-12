import type { Codec } from '../codec-runtime';
import { codec_Connection } from './Connection';

export const codec_ConnectionList_connections: Codec = {"kind": "array", "items": () => codec_Connection};
