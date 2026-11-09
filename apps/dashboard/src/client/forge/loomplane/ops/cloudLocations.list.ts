import type { OperationMeta } from '../ops';
import { codec_CloudLocationList } from '../codecs/CloudLocationList';

export const op_cloudLocations_list = {
  method: 'GET',
  path: '/api/v1/clouds/{cloudId}/locations',
  entity: 'CloudLocation',
  rootType: 'CloudLocationList',
  provides: ['CloudLocation:{id}', 'CloudLocation[]'],
  invalidates: [],
  responseCodec: codec_CloudLocationList,
} as const satisfies OperationMeta;
