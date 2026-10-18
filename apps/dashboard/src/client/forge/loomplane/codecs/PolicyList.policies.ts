import type { Codec } from '../codec-runtime';
import { codec_Policy } from './Policy';

export const codec_PolicyList_policies: Codec = {"kind": "array", "items": () => codec_Policy};
