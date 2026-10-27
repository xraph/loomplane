import type { OperationMeta } from '../ops';
import { codec_Registration } from '../codecs/Registration';
import { codec_RegistrationInputBody } from '../codecs/RegistrationInputBody';

export const op_domainRegistrations_preview = {
  method: 'POST',
  path: '/api/v1/domain-registrations/preview',
  entity: 'Registration',
  rootType: 'Registration',
  provides: ['Registration:{id}'],
  invalidates: ['Registration[]'],
  bodyCodec: codec_RegistrationInputBody,
  responseCodec: codec_Registration,
} as const satisfies OperationMeta;
