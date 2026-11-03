import type { Codec } from '../codec-runtime';
import { codec_Link } from './Link';

export const codec_Request_context: Codec = {"kind": "array", "items": () => codec_Link};
