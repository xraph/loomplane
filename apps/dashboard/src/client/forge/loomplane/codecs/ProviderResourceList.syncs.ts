import type { Codec } from '../codec-runtime';
import { codec_ProviderResourceSync } from './ProviderResourceSync';

export const codec_ProviderResourceList_syncs: Codec = {"kind": "array", "items": () => codec_ProviderResourceSync};
