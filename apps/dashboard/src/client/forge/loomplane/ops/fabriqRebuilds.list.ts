import type { OperationMeta } from '../ops';
import { codec_RebuildList } from '../codecs/RebuildList';

export const op_fabriqRebuilds_list = {
  method: 'GET',
  path: '/api/v1/projects/{projectId}/environments/{environmentId}/fabriq/{resource}/rebuilds',
  entity: 'Rebuild',
  rootType: 'RebuildList',
  provides: ['Rebuild:{id}', 'Rebuild[]'],
  invalidates: [],
  responseCodec: codec_RebuildList,
} as const satisfies OperationMeta;
