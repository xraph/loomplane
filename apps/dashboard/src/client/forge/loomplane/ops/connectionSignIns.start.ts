import type { OperationMeta } from '../ops';
import { codec_SignIn } from '../codecs/SignIn';
import { codec_SignInInputBody } from '../codecs/SignInInputBody';

export const op_connectionSignIns_start = {
  method: 'POST',
  path: '/api/v1/connections/sign-ins',
  provides: [],
  invalidates: [],
  bodyCodec: codec_SignInInputBody,
  responseCodec: codec_SignIn,
} as const satisfies OperationMeta;
