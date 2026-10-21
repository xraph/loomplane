import type { Codec } from '../codec-runtime';
import { codec_CloudprofilesCreateInputBody_default_placement } from './CloudprofilesCreateInputBody.default_placement';

export const codec_CloudprofilesCreateInputBody: Codec = {"kind": "object", "fields": {"default_placement": {"ts": "defaultPlacement", "codec": () => codec_CloudprofilesCreateInputBody_default_placement}, "name": {"ts": "name"}}, "required": ["name"]};
