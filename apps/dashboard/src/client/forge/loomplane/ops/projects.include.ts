import type { OperationMeta } from '../ops';
import { codec_IncludeInputBody } from '../codecs/IncludeInputBody';
import { codec_Project } from '../codecs/Project';

export const op_projects_include = {
  method: 'POST',
  path: '/api/v1/projects/{id}/exclusions/remove',
  entity: 'Project',
  rootType: 'Project',
  provides: ['Project:{id}'],
  invalidates: ['Project[]'],
  bodyCodec: codec_IncludeInputBody,
  responseCodec: codec_Project,
} as const satisfies OperationMeta;
