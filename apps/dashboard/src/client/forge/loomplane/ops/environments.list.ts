import type { OperationMeta } from '../ops';
import { codec_EnvironmentList } from '../codecs/EnvironmentList';

export const op_environments_list = {
  method: 'GET',
  path: '/api/v1/projects/{id}/environments',
  entity: 'Environment',
  rootType: 'EnvironmentList',
  provides: ['Environment:{id}', 'Environment[]'],
  invalidates: [],
  responseCodec: codec_EnvironmentList,
} as const satisfies OperationMeta;
