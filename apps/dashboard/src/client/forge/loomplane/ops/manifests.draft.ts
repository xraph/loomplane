import type { OperationMeta } from '../ops';
import { codec_ManifestDraft } from '../codecs/ManifestDraft';

export const op_manifests_draft = {
  method: 'GET',
  path: '/api/v1/projects/{id}/manifest/draft',
  entity: 'ManifestDraft',
  rootType: 'ManifestDraft',
  provides: ['ManifestDraft:{projectId}'],
  invalidates: [],
  responseCodec: codec_ManifestDraft,
} as const satisfies OperationMeta;
