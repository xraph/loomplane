import type { OperationMeta } from '../ops';
import { codec_CloudClusterOperation } from '../codecs/CloudClusterOperation';

export const op_clusterOperations_retry = {
  method: 'POST',
  path: '/api/v1/clouds/{cloudId}/cluster-operations/{operationId}/retry',
  entity: 'CloudClusterOperation',
  rootType: 'CloudClusterOperation',
  provides: ['CloudClusterOperation:{id}'],
  invalidates: ['CloudCluster', 'CloudClusterOperation', 'CloudClusterOperationPage', 'CloudClusterOperation[]', 'CloudClusterPage'],
  responseCodec: codec_CloudClusterOperation,
} as const satisfies OperationMeta;
