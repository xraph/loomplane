import type { Codec } from '../codec-runtime';
import { codec_RealtimeRealtimeSnapshot_records } from './RealtimeRealtimeSnapshot.records';

export const codec_RealtimeRealtimeSnapshot: Codec = {"kind": "object", "fields": {"cursor": {"ts": "cursor"}, "partial": {"ts": "partial"}, "records": {"ts": "records", "codec": () => codec_RealtimeRealtimeSnapshot_records}}, "required": ["cursor", "partial", "records"]};
