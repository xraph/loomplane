import type { Codec } from '../codec-runtime';
import { codec_ProviderKey } from './ProviderKey';

export const codec_View_provider_keys: Codec = {"kind": "array", "items": () => codec_ProviderKey};
