import type { Codec } from '../codec-runtime';
import { codec_Sync } from './Sync';

export const codec_CloudSyncList_syncs: Codec = {"kind": "array", "items": () => codec_Sync};
