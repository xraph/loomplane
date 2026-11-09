import type { Codec } from '../codec-runtime';
import { codec_DiscoveryEvidence } from './DiscoveryEvidence';

export const codec_Finding_evidence: Codec = {"kind": "array", "items": () => codec_DiscoveryEvidence};
