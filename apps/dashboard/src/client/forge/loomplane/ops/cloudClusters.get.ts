import type { OperationMeta } from '../ops';
import { codec_CloudCluster } from '../codecs/CloudCluster';

export const op_cloudClusters_get = {
  method: 'GET',
  path: '/api/v1/clouds/{cloudId}/clusters/{clusterId}',
  entity: 'CloudCluster',
  rootType: 'CloudCluster',
  provides: ['CloudCluster:{id}'],
  invalidates: [],
  responseCodec: codec_CloudCluster,
} as const satisfies OperationMeta;
