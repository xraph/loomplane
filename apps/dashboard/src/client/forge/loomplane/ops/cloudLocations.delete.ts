import type { OperationMeta } from '../ops';
import { codec_CloudLocationDeleteInputBody } from '../codecs/CloudLocationDeleteInputBody';

export const op_cloudLocations_delete = {
  method: 'DELETE',
  path: '/api/v1/clouds/{cloudId}/locations/{locationId}',
  provides: [],
  invalidates: [],
  bodyCodec: codec_CloudLocationDeleteInputBody,
} as const satisfies OperationMeta;
