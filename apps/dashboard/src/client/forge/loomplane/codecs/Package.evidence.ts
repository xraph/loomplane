import type { Codec } from '../codec-runtime';
import { codec_DiscoveryEvidence } from './DiscoveryEvidence';

export const codec_Package_evidence: Codec = {"kind": "array", "items": () => codec_DiscoveryEvidence};
