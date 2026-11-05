import type { Codec } from '../codec-runtime';
import { codec_DiscoveryEvidence } from './DiscoveryEvidence';

export const codec_BuildSystem: Codec = {"kind": "object", "fields": {"evidence": {"ts": "evidence", "codec": () => codec_DiscoveryEvidence}, "name": {"ts": "name"}, "path": {"ts": "path"}, "source": {"ts": "source"}}, "required": ["evidence", "name", "path"]};
