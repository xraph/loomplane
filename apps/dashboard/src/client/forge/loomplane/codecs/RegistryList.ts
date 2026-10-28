import type { Codec } from '../codec-runtime';
import { codec_RegistryList_counts } from './RegistryList.counts';
import { codec_RegistryList_entries } from './RegistryList.entries';

export const codec_RegistryList: Codec = {"kind": "object", "fields": {"counts": {"ts": "counts", "codec": () => codec_RegistryList_counts}, "entries": {"ts": "entries", "codec": () => codec_RegistryList_entries}, "fits_notice": {"ts": "fitsNotice"}}, "required": ["counts", "entries"]};
