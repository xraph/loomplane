import type { OperationMeta } from '../ops';
import { codec_ClusterRetirementPreviewInputBody } from '../codecs/ClusterRetirementPreviewInputBody';
import { codec_ClusterRetirementReview } from '../codecs/ClusterRetirementReview';

export const op_cloudClusters_retirementPreview = {
  method: 'POST',
  path: '/api/v1/clouds/{cloudId}/clusters/{clusterId}/retirement/preview',
  provides: [],
  invalidates: ['CloudCluster:{res.resourceId}', 'CloudClusterOperation[]', 'CloudCluster[]', 'RuntimeAttachmentPage:{res.resourceId}'],
  bodyCodec: codec_ClusterRetirementPreviewInputBody,
  responseCodec: codec_ClusterRetirementReview,
} as const satisfies OperationMeta;
