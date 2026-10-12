import type { OperationMeta } from '../ops';
import { codec_VariableList } from '../codecs/VariableList';

export const op_projectVariables_list = {
  method: 'GET',
  path: '/api/v1/projects/{id}/variables',
  entity: 'Variable',
  rootType: 'VariableList',
  provides: ['Variable:{id}', 'Variable[]'],
  invalidates: [],
  responseCodec: codec_VariableList,
} as const satisfies OperationMeta;
