import type { OperationMeta } from '../ops';
import { codec_CreateVariableInputBody } from '../codecs/CreateVariableInputBody';
import { codec_Variable } from '../codecs/Variable';

export const op_variables_create = {
  method: 'POST',
  path: '/api/v1/variables',
  entity: 'Variable',
  rootType: 'Variable',
  provides: ['Variable:{id}'],
  invalidates: ['Variable[]'],
  bodyCodec: codec_CreateVariableInputBody,
  responseCodec: codec_Variable,
} as const satisfies OperationMeta;
