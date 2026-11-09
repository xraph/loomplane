import type { OperationMeta } from '../ops';
import { codec_TemplateRelease } from '../codecs/TemplateRelease';

export const op_fleetReleases_get = {
  method: 'GET',
  path: '/api/v1/projects/{project_id}/fleet/releases/{id}',
  entity: 'TemplateRelease',
  rootType: 'TemplateRelease',
  provides: ['TemplateRelease:{id}'],
  invalidates: [],
  responseCodec: codec_TemplateRelease,
} as const satisfies OperationMeta;
