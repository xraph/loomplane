import type { OperationMeta } from '../ops';
import { codec_SignInAppList } from '../codecs/SignInAppList';

export const op_cloudSignInApps_list = {
  method: 'GET',
  path: '/api/v1/cloud-sign-in/apps',
  provides: [],
  invalidates: [],
  responseCodec: codec_SignInAppList,
} as const satisfies OperationMeta;
