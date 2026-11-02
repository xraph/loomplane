import type { Codec } from '../codec-runtime';
import { codec_ExcludeInputBody_exclusions } from './ExcludeInputBody.exclusions';

export const codec_ExcludeInputBody: Codec = {"kind": "object", "fields": {"exclusions": {"ts": "exclusions", "codec": () => codec_ExcludeInputBody_exclusions}}, "required": ["exclusions"]};
