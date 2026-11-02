import type { Codec } from '../codec-runtime';
import { codec_Output } from './Output';

export const codec_CompositionConfig_outputs: Codec = {"kind": "array", "items": () => codec_Output};
