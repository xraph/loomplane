import type { OperationMeta } from '../ops';
import { codec_CloudClusterPage } from '../codecs/CloudClusterPage';

export const op_cloudClusters_list = {
  method: 'GET',
  path: '/api/v1/clouds/{cloudId}/clusters',
  entity: 'CloudCluster',
  rootType: 'CloudClusterPage',
  provides: ['CloudCluster:{id}', 'CloudCluster[]'],
  invalidates: [],
  responseCodec: codec_CloudClusterPage,
} as const satisfies OperationMeta;
