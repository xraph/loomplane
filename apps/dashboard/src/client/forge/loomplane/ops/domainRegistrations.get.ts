import type { OperationMeta } from '../ops';
import { codec_Registration } from '../codecs/Registration';

export const op_domainRegistrations_get = {
  method: 'GET',
  path: '/api/v1/domain-registrations/{registrationId}',
  entity: 'Registration',
  rootType: 'Registration',
  provides: ['Registration:{id}'],
  invalidates: [],
  responseCodec: codec_Registration,
} as const satisfies OperationMeta;
