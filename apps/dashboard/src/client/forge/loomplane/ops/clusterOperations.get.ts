import type { OperationMeta } from '../ops';
import { codec_CloudClusterOperation } from '../codecs/CloudClusterOperation';

export const op_clusterOperations_get = {
  method: 'GET',
  path: '/api/v1/clouds/{cloudId}/cluster-operations/{operationId}',
  entity: 'CloudClusterOperation',
  rootType: 'CloudClusterOperation',
  provides: ['CloudClusterOperation:{id}'],
  invalidates: [],
  responseCodec: codec_CloudClusterOperation,
} as const satisfies OperationMeta;
