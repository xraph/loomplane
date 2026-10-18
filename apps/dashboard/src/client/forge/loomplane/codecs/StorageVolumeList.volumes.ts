import type { Codec } from '../codec-runtime';
import { codec_StoredVolume } from './StoredVolume';

export const codec_StorageVolumeList_volumes: Codec = {"kind": "array", "items": () => codec_StoredVolume};
