import type { Codec } from '../codec-runtime';
import { codec_Log } from './Log';

export const codec_LogPage_logs: Codec = {"kind": "array", "items": () => codec_Log};
