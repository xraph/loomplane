import type { OperationMeta } from '../ops';
import { codec_DeployedList } from '../codecs/DeployedList';

export const op_deployed_list = {
  method: 'GET',
  path: '/api/v1/projects/{id}/environments/{environment}/deployed',
  entity: 'DeployedRepository',
  rootType: 'DeployedList',
  provides: ['DeployedRepository:{sourceId}', 'DeployedRepository[]'],
  invalidates: [],
  responseCodec: codec_DeployedList,
} as const satisfies OperationMeta;
