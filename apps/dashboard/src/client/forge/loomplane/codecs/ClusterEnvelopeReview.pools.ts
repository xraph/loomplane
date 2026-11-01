import type { Codec } from '../codec-runtime';
import { codec_ClusterPoolEnvelopeReview } from './ClusterPoolEnvelopeReview';

export const codec_ClusterEnvelopeReview_pools: Codec = {"kind": "array", "items": () => codec_ClusterPoolEnvelopeReview};
