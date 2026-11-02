import type { Codec } from '../codec-runtime';
import { codec_IncludeInputBody_exclusions } from './IncludeInputBody.exclusions';

export const codec_IncludeInputBody: Codec = {"kind": "object", "fields": {"exclusions": {"ts": "exclusions", "codec": () => codec_IncludeInputBody_exclusions}}, "required": ["exclusions"]};
