import type { Codec } from '../codec-runtime';
import { codec_DiscoveryEvidence } from './DiscoveryEvidence';

export const codec_Requirement: Codec = {"kind": "object", "fields": {"evidence": {"ts": "evidence", "codec": () => codec_DiscoveryEvidence}, "git": {"ts": "git"}, "name": {"ts": "name"}, "version": {"ts": "version"}}, "required": ["evidence", "name"]};
