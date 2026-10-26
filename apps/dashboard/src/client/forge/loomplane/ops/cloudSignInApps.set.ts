import type { OperationMeta } from '../ops';
import { codec_AppInputBody } from '../codecs/AppInputBody';
import { codec_SignInApp } from '../codecs/SignInApp';

export const op_cloudSignInApps_set = {
  method: 'PUT',
  path: '/api/v1/cloud-sign-in/apps/{cloud}',
  provides: [],
  invalidates: [],
  bodyCodec: codec_AppInputBody,
  responseCodec: codec_SignInApp,
} as const satisfies OperationMeta;
