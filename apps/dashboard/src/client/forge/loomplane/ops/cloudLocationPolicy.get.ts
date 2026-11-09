import type { OperationMeta } from '../ops';
import { codec_LocationPolicy } from '../codecs/LocationPolicy';

export const op_cloudLocationPolicy_get = {
  method: 'GET',
  path: '/api/v1/clouds/{cloudId}/location-policy',
  provides: [],
  invalidates: [],
  responseCodec: codec_LocationPolicy,
} as const satisfies OperationMeta;
