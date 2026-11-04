import type { Codec } from '../codec-runtime';
import { codec_ManifestUse } from './ManifestUse';

export const codec_EditInputBody_set_use: Codec = {"kind": "object", "fields": {"dependency": {"ts": "dependency"}, "resource": {"ts": "resource"}, "use": {"ts": "use", "codec": () => codec_ManifestUse}}, "required": ["dependency", "resource", "use"]};
