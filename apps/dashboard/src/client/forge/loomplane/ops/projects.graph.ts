import type { OperationMeta } from '../ops';
import { codec_ProjectView } from '../codecs/ProjectView';

export const op_projects_graph = {
  method: 'GET',
  path: '/api/v1/projects/{id}/graph',
  entity: 'ProjectView',
  rootType: 'ProjectView',
  provides: ['ProjectView:{projectId}'],
  invalidates: [],
  responseCodec: codec_ProjectView,
} as const satisfies OperationMeta;
