import type { OperationMeta } from '../ops';
import { codec_Project } from '../codecs/Project';

export const op_projects_get = {
  method: 'GET',
  path: '/api/v1/projects/{id}',
  entity: 'Project',
  rootType: 'Project',
  provides: ['Project:{id}'],
  invalidates: [],
  responseCodec: codec_Project,
} as const satisfies OperationMeta;
