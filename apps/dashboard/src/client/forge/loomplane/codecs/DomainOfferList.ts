import type { Codec } from '../codec-runtime';
import { codec_DomainOfferList_offers } from './DomainOfferList.offers';

export const codec_DomainOfferList: Codec = {"kind": "object", "fields": {"offers": {"ts": "offers", "codec": () => codec_DomainOfferList_offers}}, "required": ["offers"]};
