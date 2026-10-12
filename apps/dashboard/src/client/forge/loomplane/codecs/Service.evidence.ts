import type { Codec } from '../codec-runtime';
import { codec_Evidence } from './Evidence';

export const codec_Service_evidence: Codec = {"kind": "array", "items": () => codec_Evidence};
