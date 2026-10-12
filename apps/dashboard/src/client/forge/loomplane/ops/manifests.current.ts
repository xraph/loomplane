import type { OperationMeta } from '../ops';
import { codec_Revision } from '../codecs/Revision';

export const op_manifests_current = {
  method: 'GET',
  path: '/api/v1/projects/{id}/manifest',
  entity: 'Revision',
  rootType: 'Revision',
  provides: ['Revision:{id}'],
  invalidates: [],
  responseCodec: codec_Revision,
} as const satisfies OperationMeta;
