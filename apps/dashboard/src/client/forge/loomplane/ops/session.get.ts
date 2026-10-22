import type { OperationMeta } from '../ops';
import { codec_IdentitySession } from '../codecs/IdentitySession';

export const op_session_get = {
  method: 'GET',
  path: '/api/v1/session',
  provides: [],
  invalidates: [],
  responseCodec: codec_IdentitySession,
} as const satisfies OperationMeta;
