import type { OperationMeta } from '../ops';
import { codec_CloudLocation } from '../codecs/CloudLocation';
import { codec_LocationVerificationInputBody } from '../codecs/LocationVerificationInputBody';

export const op_cloudLocations_verify = {
  method: 'POST',
  path: '/api/v1/clouds/{cloudId}/locations/{locationId}/verification',
  entity: 'CloudLocation',
  rootType: 'CloudLocation',
  provides: ['CloudLocation:{id}'],
  invalidates: ['CloudLocation[]'],
  bodyCodec: codec_LocationVerificationInputBody,
  responseCodec: codec_CloudLocation,
} as const satisfies OperationMeta;
