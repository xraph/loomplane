import type { OperationMeta } from '../ops';
import { codec_CloudLocationList } from '../codecs/CloudLocationList';

export const op_projectCloudLocations_list = {
  method: 'GET',
  path: '/api/v1/projects/{id}/clouds/{cloudId}/locations',
  entity: 'CloudLocation',
  rootType: 'CloudLocationList',
  provides: ['CloudLocation:{id}', 'CloudLocation[]'],
  invalidates: [],
  responseCodec: codec_CloudLocationList,
} as const satisfies OperationMeta;
