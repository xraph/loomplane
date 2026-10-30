import type { OperationMeta } from '../ops';
import { codec_ClusterOptions } from '../codecs/ClusterOptions';

export const op_cloudClusters_options = {
  method: 'GET',
  path: '/api/v1/clouds/{cloudId}/cluster-options/{connectionId}',
  entity: 'ClusterNodeSize',
  rootType: 'ClusterOptions',
  provides: ['ClusterNodeSize:{id}', 'ClusterNodeSize[]'],
  invalidates: [],
  responseCodec: codec_ClusterOptions,
} as const satisfies OperationMeta;
