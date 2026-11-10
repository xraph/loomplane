import type { Codec } from '../codec-runtime';
import { codec_FlagList_flags } from './FlagList.flags';

export const codec_FlagList: Codec = {"kind": "object", "fields": {"flags": {"ts": "flags", "codec": () => codec_FlagList_flags}}, "required": ["flags"]};
