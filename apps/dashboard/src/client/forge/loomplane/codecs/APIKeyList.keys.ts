import type { Codec } from '../codec-runtime';
import { codec_APIKey } from './APIKey';

export const codec_APIKeyList_keys: Codec = {"kind": "array", "items": () => codec_APIKey};
