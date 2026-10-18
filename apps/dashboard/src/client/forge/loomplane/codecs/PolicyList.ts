import type { Codec } from '../codec-runtime';
import { codec_PolicyList_policies } from './PolicyList.policies';

export const codec_PolicyList: Codec = {"kind": "object", "fields": {"policies": {"ts": "policies", "codec": () => codec_PolicyList_policies}}, "required": ["policies"]};
