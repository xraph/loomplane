import type { Codec } from '../codec-runtime';
import { codec_ExclusionRule } from './ExclusionRule';
import { codec_Service } from './Service';

export const codec_Excluded: Codec = {"kind": "object", "fields": {"rule": {"ts": "rule", "codec": () => codec_ExclusionRule}, "service": {"ts": "service", "codec": () => codec_Service}}, "required": ["rule", "service"]};
