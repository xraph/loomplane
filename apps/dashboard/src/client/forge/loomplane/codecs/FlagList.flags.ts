import type { Codec } from '../codec-runtime';
import { codec_Flag } from './Flag';

export const codec_FlagList_flags: Codec = {"kind": "array", "items": () => codec_Flag};
