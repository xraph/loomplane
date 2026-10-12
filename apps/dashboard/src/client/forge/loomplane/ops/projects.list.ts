import type { OperationMeta } from '../ops';
import { codec_ProjectList } from '../codecs/ProjectList';

export const op_projects_list = {
  method: 'GET',
  path: '/api/v1/projects',
  entity: 'Project',
  rootType: 'ProjectList',
  provides: ['Project:{id}', 'Project[]'],
  invalidates: [],
  responseCodec: codec_ProjectList,
} as const satisfies OperationMeta;
