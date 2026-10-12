import type { OperationMeta } from '../ops';
import { codec_CloudList } from '../codecs/CloudList';

export const op_connectionClouds_list = {
  method: 'GET',
  path: '/api/v1/connection-clouds',
  provides: [],
  invalidates: [],
  responseCodec: codec_CloudList,
} as const satisfies OperationMeta;
