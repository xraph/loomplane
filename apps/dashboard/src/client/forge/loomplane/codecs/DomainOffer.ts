import type { Codec } from '../codec-runtime';
import { codec_DomainPrice } from './DomainPrice';

export const codec_DomainOffer: Codec = {"kind": "object", "fields": {"name": {"ts": "name"}, "price": {"ts": "price", "codec": () => codec_DomainPrice}, "reason": {"ts": "reason"}, "registrable": {"ts": "registrable"}, "tier": {"ts": "tier"}}, "required": ["name", "registrable"]};
