import type { Codec } from '../codec-runtime';
import { codec_APIKeyList_keys } from './APIKeyList.keys';

export const codec_APIKeyList: Codec = {"kind": "object", "fields": {"keys": {"ts": "keys", "codec": () => codec_APIKeyList_keys}}, "required": ["keys"]};
