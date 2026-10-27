import type { OperationMeta } from '../ops';
import { codec_RegistrationExtensionList } from '../codecs/RegistrationExtensionList';

export const op_domainRegistration_extensions = {
  method: 'GET',
  path: '/api/v1/domain-registration/extensions',
  provides: [],
  invalidates: [],
  responseCodec: codec_RegistrationExtensionList,
} as const satisfies OperationMeta;
