import type { OperationMeta } from '../ops';
import { codec_BindingList } from '../codecs/BindingList';

export const op_bindings_list = {
  method: 'GET',
  path: '/api/v1/projects/{id}/bindings',
  entity: 'Binding',
  rootType: 'BindingList',
  provides: ['Binding:{id}', 'Binding[]'],
  invalidates: [],
  responseCodec: codec_BindingList,
} as const satisfies OperationMeta;
