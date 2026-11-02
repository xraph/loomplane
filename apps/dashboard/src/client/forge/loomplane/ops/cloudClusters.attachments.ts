import type { OperationMeta } from '../ops';
import { codec_RuntimeAttachmentPage } from '../codecs/RuntimeAttachmentPage';

export const op_cloudClusters_attachments = {
  method: 'GET',
  path: '/api/v1/clouds/{cloudId}/clusters/{clusterId}/attachments',
  entity: 'RuntimeAttachmentPage',
  rootType: 'RuntimeAttachmentPage',
  provides: ['RuntimeAttachmentPage:{clusterId}'],
  invalidates: [],
  responseCodec: codec_RuntimeAttachmentPage,
} as const satisfies OperationMeta;
