import type { OperationMeta } from '../ops';
import { codec_ClusterCapacity } from '../codecs/ClusterCapacity';

export const op_cloudClusters_capacity = {
  method: 'GET',
  path: '/api/v1/clouds/{cloudId}/clusters/{clusterId}/capacity',
  entity: 'ClusterCapacity',
  rootType: 'ClusterCapacity',
  provides: ['ClusterCapacity:{resourceId}'],
  invalidates: [],
  responseCodec: codec_ClusterCapacity,
} as const satisfies OperationMeta;
