import type { Codec } from '../codec-runtime';
import { codec_FabriqKeyList_keys } from './FabriqKeyList.keys';

export const codec_FabriqKeyList: Codec = {"kind": "object", "fields": {"keys": {"ts": "keys", "codec": () => codec_FabriqKeyList_keys}}, "required": ["keys"]};
