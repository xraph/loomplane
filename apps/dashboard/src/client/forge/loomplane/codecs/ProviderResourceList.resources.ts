import type { Codec } from '../codec-runtime';
import { codec_ProviderResource } from './ProviderResource';

export const codec_ProviderResourceList_resources: Codec = {"kind": "array", "items": () => codec_ProviderResource};
