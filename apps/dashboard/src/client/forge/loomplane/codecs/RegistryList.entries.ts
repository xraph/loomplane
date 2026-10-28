import type { Codec } from '../codec-runtime';
import { codec_EntrySummary } from './EntrySummary';

export const codec_RegistryList_entries: Codec = {"kind": "array", "items": () => codec_EntrySummary};
