import type { OperationMeta } from '../ops';
import { codec_CloudLocation } from '../codecs/CloudLocation';

export const op_cloudLocations_get = {
  method: 'GET',
  path: '/api/v1/clouds/{cloudId}/locations/{locationId}',
  entity: 'CloudLocation',
  rootType: 'CloudLocation',
  provides: ['CloudLocation:{id}'],
  invalidates: [],
  responseCodec: codec_CloudLocation,
} as const satisfies OperationMeta;
