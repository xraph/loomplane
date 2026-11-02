import type { Codec } from '../codec-runtime';
import { codec_DomainOffer } from './DomainOffer';

export const codec_DomainOfferList_offers: Codec = {"kind": "array", "items": () => codec_DomainOffer};
