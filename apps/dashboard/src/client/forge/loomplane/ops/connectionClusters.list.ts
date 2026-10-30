import type { OperationMeta } from '../ops';
import { codec_ClusterPage } from '../codecs/ClusterPage';

export const op_connectionClusters_list = {
  method: 'GET',
  path: '/api/v1/connections/{connectionId}/clusters',
  provides: [],
  invalidates: [],
  responseCodec: codec_ClusterPage,
} as const satisfies OperationMeta;
