import type { Codec } from '../codec-runtime';
import { codec_RequestList_requests } from './RequestList.requests';

export const codec_RequestList: Codec = {"kind": "object", "fields": {"requests": {"ts": "requests", "codec": () => codec_RequestList_requests}}, "required": ["requests"]};
