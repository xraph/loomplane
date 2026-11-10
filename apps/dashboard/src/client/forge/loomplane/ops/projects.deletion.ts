import type { OperationMeta } from '../ops';
import { codec_DeletionPreview } from '../codecs/DeletionPreview';

export const op_projects_deletion = {
  method: 'GET',
  path: '/api/v1/projects/{id}/deletion',
  provides: [],
  invalidates: [],
  responseCodec: codec_DeletionPreview,
} as const satisfies OperationMeta;
