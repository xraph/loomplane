import type { OperationMeta } from '../ops';
import { codec_CloudLocation } from '../codecs/CloudLocation';
import { codec_LocationInputBody } from '../codecs/LocationInputBody';

export const op_cloudLocations_save = {
  method: 'POST',
  path: '/api/v1/clouds/{cloudId}/locations',
  entity: 'CloudLocation',
  rootType: 'CloudLocation',
  provides: ['CloudLocation:{id}'],
  invalidates: ['CloudLocation[]'],
  bodyCodec: codec_LocationInputBody,
  responseCodec: codec_CloudLocation,
} as const satisfies OperationMeta;
