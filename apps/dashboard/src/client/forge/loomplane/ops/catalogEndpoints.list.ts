import type { OperationMeta } from '../ops';
import { codec_EndpointList } from '../codecs/EndpointList';

export const op_catalogEndpoints_list = {
  method: 'GET',
  path: '/api/v1/catalog/endpoints',
  provides: [],
  invalidates: [],
  responseCodec: codec_EndpointList,
} as const satisfies OperationMeta;
