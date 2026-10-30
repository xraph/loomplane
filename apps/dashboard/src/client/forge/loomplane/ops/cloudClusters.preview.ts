import type { OperationMeta } from '../ops';
import { codec_CloudClusterOperation } from '../codecs/CloudClusterOperation';
import { codec_ClusterInputBody } from '../codecs/ClusterInputBody';

export const op_cloudClusters_preview = {
  method: 'POST',
  path: '/api/v1/clouds/{cloudId}/clusters/preview',
  entity: 'CloudClusterOperation',
  rootType: 'CloudClusterOperation',
  provides: ['CloudClusterOperation:{id}'],
  invalidates: ['CloudCluster', 'CloudClusterOperation', 'CloudClusterOperationPage', 'CloudClusterOperation[]', 'CloudClusterPage'],
  bodyCodec: codec_ClusterInputBody,
  responseCodec: codec_CloudClusterOperation,
} as const satisfies OperationMeta;
