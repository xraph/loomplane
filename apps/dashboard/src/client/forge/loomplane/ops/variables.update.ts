import type { OperationMeta } from '../ops';
import { codec_UpdateVariableInputBody } from '../codecs/UpdateVariableInputBody';
import { codec_Variable } from '../codecs/Variable';

export const op_variables_update = {
  method: 'PATCH',
  path: '/api/v1/variables/{id}',
  entity: 'Variable',
  rootType: 'Variable',
  provides: ['Variable:{id}'],
  invalidates: ['Variable[]'],
  bodyCodec: codec_UpdateVariableInputBody,
  responseCodec: codec_Variable,
} as const satisfies OperationMeta;
