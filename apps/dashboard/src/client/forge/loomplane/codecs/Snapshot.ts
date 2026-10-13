import type { Codec } from '../codec-runtime';
import { codec_Snapshot_records } from './Snapshot.records';

export const codec_Snapshot: Codec = {"kind": "object", "fields": {"cursor": {"ts": "cursor"}, "partial": {"ts": "partial"}, "records": {"ts": "records", "codec": () => codec_Snapshot_records}}, "required": ["cursor", "partial", "records"]};
