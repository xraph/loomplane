import type { OperationMeta } from '../ops';
import { codec_CloudClusterOperationPage } from '../codecs/CloudClusterOperationPage';

export const op_clusterOperations_list = {
  method: 'GET',
  path: '/api/v1/clouds/{cloudId}/cluster-operations',
  entity: 'CloudClusterOperation',
  rootType: 'CloudClusterOperationPage',
  provides: ['CloudClusterOperation:{id}', 'CloudClusterOperation[]'],
  invalidates: [],
  responseCodec: codec_CloudClusterOperationPage,
} as const satisfies OperationMeta;
