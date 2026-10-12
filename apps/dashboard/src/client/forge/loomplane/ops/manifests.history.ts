import type { OperationMeta } from '../ops';
import { codec_RevisionList } from '../codecs/RevisionList';

export const op_manifests_history = {
  method: 'GET',
  path: '/api/v1/projects/{id}/manifest/revisions',
  entity: 'Revision',
  rootType: 'RevisionList',
  provides: ['Revision:{id}', 'Revision[]'],
  invalidates: [],
  responseCodec: codec_RevisionList,
} as const satisfies OperationMeta;
