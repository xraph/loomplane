import type { Codec } from '../codec-runtime';
import { codec_ClusterEnvelopeResources } from './ClusterEnvelopeResources';
import { codec_MoneyEstimate } from './MoneyEstimate';

export const codec_ClusterPoolEnvelopeReview: Codec = {"kind": "object", "fields": {"baseline": {"ts": "baseline", "codec": () => codec_ClusterEnvelopeResources}, "initial": {"ts": "initial", "codec": () => codec_ClusterEnvelopeResources}, "maximum": {"ts": "maximum", "codec": () => codec_ClusterEnvelopeResources}, "money": {"ts": "money", "codec": () => codec_MoneyEstimate}, "pool_id": {"ts": "poolId"}}, "required": ["baseline", "initial", "maximum", "money", "pool_id"]};
