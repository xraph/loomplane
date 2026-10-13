import type { Codec } from '../codec-runtime';
import { codec_LogPage_logs } from './LogPage.logs';

export const codec_LogPage: Codec = {"kind": "object", "fields": {"gap": {"ts": "gap"}, "logs": {"ts": "logs", "codec": () => codec_LogPage_logs}, "next": {"ts": "next"}}, "required": ["gap", "logs", "next"]};
