import type { OperationMeta } from '../ops';
import { codec_RegistrationList } from '../codecs/RegistrationList';

export const op_domainRegistrations_list = {
  method: 'GET',
  path: '/api/v1/domain-registrations',
  provides: [],
  invalidates: [],
  responseCodec: codec_RegistrationList,
} as const satisfies OperationMeta;
