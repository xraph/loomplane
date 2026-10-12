import type { OperationMeta } from '../ops';
import { codec_Project } from '../codecs/Project';
import { codec_UpdateProjectInputBody } from '../codecs/UpdateProjectInputBody';

export const op_projects_update = {
  method: 'PATCH',
  path: '/api/v1/projects/{id}',
  entity: 'Project',
  rootType: 'Project',
  provides: ['Project:{id}'],
  invalidates: ['Project[]'],
  bodyCodec: codec_UpdateProjectInputBody,
  responseCodec: codec_Project,
} as const satisfies OperationMeta;
