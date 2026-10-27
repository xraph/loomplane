import type { OperationMeta } from '../ops';
import { codec_AutoRenewInputBody } from '../codecs/AutoRenewInputBody';
import { codec_Registration } from '../codecs/Registration';

export const op_domainRegistrations_setAutoRenew = {
  method: 'POST',
  path: '/api/v1/domain-registrations/{registrationId}/auto-renew',
  entity: 'Registration',
  rootType: 'Registration',
  provides: ['Registration:{id}'],
  invalidates: ['Registration[]'],
  bodyCodec: codec_AutoRenewInputBody,
  responseCodec: codec_Registration,
} as const satisfies OperationMeta;
