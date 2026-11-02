import type { Codec } from '../codec-runtime';
import { codec_ConsumerKey } from './ConsumerKey';

export const codec_FabriqKeyList_keys: Codec = {"kind": "array", "items": () => codec_ConsumerKey};
