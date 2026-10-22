import type { Codec } from '../codec-runtime';
import { codec_SetDefaultPlacementInputBody_default_placement } from './SetDefaultPlacementInputBody.default_placement';

export const codec_SetDefaultPlacementInputBody: Codec = {"kind": "object", "fields": {"default_placement": {"ts": "defaultPlacement", "codec": () => codec_SetDefaultPlacementInputBody_default_placement}, "revision": {"ts": "revision"}}, "required": ["default_placement", "revision"]};
