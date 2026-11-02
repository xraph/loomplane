import type { OperationMeta } from '../ops';
import { codec_ExcludeInputBody } from '../codecs/ExcludeInputBody';
import { codec_Project } from '../codecs/Project';

export const op_projects_exclude = {
  method: 'POST',
  path: '/api/v1/projects/{id}/exclusions',
  entity: 'Project',
  rootType: 'Project',
  provides: ['Project:{id}'],
  invalidates: ['Project[]'],
  bodyCodec: codec_ExcludeInputBody,
  responseCodec: codec_Project,
} as const satisfies OperationMeta;
