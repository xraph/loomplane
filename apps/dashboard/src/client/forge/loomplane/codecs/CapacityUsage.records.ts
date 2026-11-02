import type { Codec } from '../codec-runtime';
import { codec_CapacityRecord } from './CapacityRecord';

export const codec_CapacityUsage_records: Codec = {"kind": "array", "items": () => codec_CapacityRecord};
