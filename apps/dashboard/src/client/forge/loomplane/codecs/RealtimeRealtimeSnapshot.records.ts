import type { Codec } from '../codec-runtime';
import { codec_SnapshotRecord } from './SnapshotRecord';

export const codec_RealtimeRealtimeSnapshot_records: Codec = {"kind": "array", "items": () => codec_SnapshotRecord};
