import type { OperationMeta } from '../ops';
import { codec_Session } from '../codecs/Session';

export const op_session_get = {
  method: 'GET',
  path: '/api/v1/session',
  provides: [],
  invalidates: [],
  responseCodec: codec_Session,
} as const satisfies OperationMeta;
