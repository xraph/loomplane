import type { Codec } from '../codec-runtime';
import { codec_Server } from './Server';

export const codec_ServerList_servers: Codec = {"kind": "array", "items": () => codec_Server};
