import type { OperationMeta } from '../ops';
import { codec_ConnectionsCloudList } from '../codecs/ConnectionsCloudList';

export const op_connectionClouds_list = {
  method: 'GET',
  path: '/api/v1/connection-clouds',
  provides: [],
  invalidates: [],
  responseCodec: codec_ConnectionsCloudList,
} as const satisfies OperationMeta;
