import type { Codec } from '../codec-runtime';
import { codec_Provider } from './Provider';

export const codec_ProviderList_providers: Codec = {"kind": "array", "items": () => codec_Provider};
