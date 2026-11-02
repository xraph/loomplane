import type { OperationMeta } from '../ops';
import { codec_DomainCheckInputBody } from '../codecs/DomainCheckInputBody';
import { codec_DomainOfferList } from '../codecs/DomainOfferList';

export const op_domainRegistration_check = {
  method: 'POST',
  path: '/api/v1/domain-registration/check',
  provides: [],
  invalidates: [],
  bodyCodec: codec_DomainCheckInputBody,
  responseCodec: codec_DomainOfferList,
} as const satisfies OperationMeta;
