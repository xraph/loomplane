import type { OperationMeta } from '../ops';
import { codec_ClusterCapacity } from '../codecs/ClusterCapacity';

export const op_cloudClusters_refreshCapacity = {
  method: 'POST',
  path: '/api/v1/clouds/{cloudId}/clusters/{clusterId}/capacity/refresh',
  entity: 'ClusterCapacity',
  rootType: 'ClusterCapacity',
  provides: ['ClusterCapacity:{resourceId}'],
  invalidates: ['CloudCluster', 'CloudClusterOperation', 'CloudClusterOperationPage', 'CloudClusterPage', 'ClusterCapacity[]'],
  responseCodec: codec_ClusterCapacity,
} as const satisfies OperationMeta;
