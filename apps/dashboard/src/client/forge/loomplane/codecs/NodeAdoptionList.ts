import type { Codec } from '../codec-runtime';
import { codec_NodeAdoptionList_adoptions } from './NodeAdoptionList.adoptions';

export const codec_NodeAdoptionList: Codec = {"kind": "object", "fields": {"adoptions": {"ts": "adoptions", "codec": () => codec_NodeAdoptionList_adoptions}}, "required": ["adoptions"]};
