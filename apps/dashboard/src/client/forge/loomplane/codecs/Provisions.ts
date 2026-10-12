import type { Codec } from '../codec-runtime';
import { codec_Provisions_versions } from './Provisions.versions';

export const codec_Provisions: Codec = {"kind": "object", "fields": {"creates": {"ts": "creates"}, "kind": {"ts": "kind"}, "versions": {"ts": "versions", "codec": () => codec_Provisions_versions}}, "required": ["creates", "kind"]};
