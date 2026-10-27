import type { OperationMeta } from '../ops';
import { codec_DomainOfferList } from '../codecs/DomainOfferList';

export const op_domainRegistration_search = {
  method: 'GET',
  path: '/api/v1/domain-registration/search',
  provides: [],
  invalidates: [],
  responseCodec: codec_DomainOfferList,
} as const satisfies OperationMeta;
