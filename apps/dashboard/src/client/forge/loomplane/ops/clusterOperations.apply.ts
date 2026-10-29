import type { OperationMeta } from '../ops';
import { codec_ApplyInputBody } from '../codecs/ApplyInputBody';
import { codec_CloudClusterOperation } from '../codecs/CloudClusterOperation';

export const op_clusterOperations_apply = {
  method: 'POST',
  path: '/api/v1/clouds/{cloudId}/cluster-operations/{operationId}/apply',
  entity: 'CloudClusterOperation',
  rootType: 'CloudClusterOperation',
  provides: ['CloudClusterOperation:{id}'],
  invalidates: ['CloudCluster', 'CloudClusterOperation', 'CloudClusterOperationPage', 'CloudClusterOperation[]', 'CloudClusterPage'],
  bodyCodec: codec_ApplyInputBody,
  responseCodec: codec_CloudClusterOperation,
} as const satisfies OperationMeta;
