import type { Codec } from '../codec-runtime';
import { codec_CapacityAccountSnapshot } from './CapacityAccountSnapshot';

export const codec_CapacityUsage_accounts: Codec = {"kind": "array", "items": () => codec_CapacityAccountSnapshot};
