import type { Codec } from '../codec-runtime';
import { codec_UpdateTargetInputBody_tags } from './UpdateTargetInputBody.tags';

export const codec_UpdateTargetInputBody: Codec = {"kind": "object", "fields": {"display_name": {"ts": "displayName"}, "revision": {"ts": "revision"}, "tags": {"ts": "tags", "codec": () => codec_UpdateTargetInputBody_tags}}, "required": ["display_name", "revision"]};
