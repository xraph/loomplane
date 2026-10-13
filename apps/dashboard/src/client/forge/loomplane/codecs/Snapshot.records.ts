import type { Codec } from '../codec-runtime';
import { codec_SnapshotRecord } from './SnapshotRecord';

export const codec_Snapshot_records: Codec = {"kind": "array", "items": () => codec_SnapshotRecord};
