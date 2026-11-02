import type { Codec } from '../codec-runtime';
import { codec_ClusterSchedulingProfile } from './ClusterSchedulingProfile';
import { codec_ClusterWorkloadVector } from './ClusterWorkloadVector';

export const codec_ClusterDemand: Codec = {"kind": "object", "fields": {"maximum": {"ts": "maximum", "codec": () => codec_ClusterWorkloadVector}, "mode": {"ts": "mode"}, "profile": {"ts": "profile", "codec": () => codec_ClusterSchedulingProfile}}, "required": ["maximum", "mode"]};
