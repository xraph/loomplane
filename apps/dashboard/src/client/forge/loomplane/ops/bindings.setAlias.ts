import type { OperationMeta } from '../ops';
import { codec_Binding } from '../codecs/Binding';
import { codec_BindingAliasInputBody } from '../codecs/BindingAliasInputBody';

export const op_bindings_setAlias = {
  method: 'PATCH',
  path: '/api/v1/projects/{id}/bindings/{binding}',
  entity: 'Binding',
  rootType: 'Binding',
  provides: ['Binding:{id}'],
  invalidates: ['Binding[]'],
  bodyCodec: codec_BindingAliasInputBody,
  responseCodec: codec_Binding,
} as const satisfies OperationMeta;
