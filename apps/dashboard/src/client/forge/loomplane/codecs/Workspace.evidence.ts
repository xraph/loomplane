import type { Codec } from '../codec-runtime';
import { codec_DiscoveryEvidence } from './DiscoveryEvidence';

export const codec_Workspace_evidence: Codec = {"kind": "array", "items": () => codec_DiscoveryEvidence};
