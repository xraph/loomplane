import type { Codec } from '../codec-runtime';
import { codec_CloudProjectGrantList_grants } from './CloudProjectGrantList.grants';

export const codec_CloudProjectGrantList: Codec = {"kind": "object", "fields": {"grants": {"ts": "grants", "codec": () => codec_CloudProjectGrantList_grants}}, "required": ["grants"]};
