import type { OperationMeta } from '../ops';
import { codec_Registration } from '../codecs/Registration';
import { codec_RegistrationApprovalBody } from '../codecs/RegistrationApprovalBody';

export const op_domainRegistrations_approve = {
  method: 'POST',
  path: '/api/v1/domain-registrations/{registrationId}/approve',
  entity: 'Registration',
  rootType: 'Registration',
  provides: ['Registration:{id}'],
  invalidates: ['Registration[]'],
  bodyCodec: codec_RegistrationApprovalBody,
  responseCodec: codec_Registration,
} as const satisfies OperationMeta;
