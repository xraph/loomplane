import type { Codec } from '../codec-runtime';
import { codec_GeneratedSecret } from './GeneratedSecret';

export const codec_CompositionConfig_secrets: Codec = {"kind": "array", "items": () => codec_GeneratedSecret};
