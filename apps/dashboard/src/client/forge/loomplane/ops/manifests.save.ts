import type { OperationMeta } from '../ops';
import { codec_Revision } from '../codecs/Revision';
import { codec_SaveInputBody } from '../codecs/SaveInputBody';

export const op_manifests_save = {
  method: 'PUT',
  path: '/api/v1/projects/{id}/manifest',
  entity: 'Revision',
  rootType: 'Revision',
  provides: ['Revision:{id}'],
  invalidates: ['Revision[]'],
  bodyCodec: codec_SaveInputBody,
  responseCodec: codec_Revision,
} as const satisfies OperationMeta;
