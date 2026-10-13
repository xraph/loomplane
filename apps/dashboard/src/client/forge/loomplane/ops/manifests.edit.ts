import type { OperationMeta } from '../ops';
import { codec_EditInputBody } from '../codecs/EditInputBody';
import { codec_Revision } from '../codecs/Revision';

export const op_manifests_edit = {
  method: 'POST',
  path: '/api/v1/projects/{id}/manifest/edits',
  entity: 'Revision',
  rootType: 'Revision',
  provides: ['Revision:{id}'],
  invalidates: ['Revision[]'],
  bodyCodec: codec_EditInputBody,
  responseCodec: codec_Revision,
} as const satisfies OperationMeta;
