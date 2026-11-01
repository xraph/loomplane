import type { Codec } from '../codec-runtime';
import { codec_ClusterEnvelopeReview_pools } from './ClusterEnvelopeReview.pools';

export const codec_ClusterEnvelopeReview: Codec = {"kind": "object", "fields": {"automatic": {"ts": "automatic"}, "pools": {"ts": "pools", "codec": () => codec_ClusterEnvelopeReview_pools}}, "required": ["automatic", "pools"]};
