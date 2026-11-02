import type { OperationMeta } from '../ops';
import { codec_Plan } from '../codecs/Plan';
import { codec_RollbackInputBody } from '../codecs/RollbackInputBody';

export const op_rollbacks_create = {
  method: 'POST',
  path: '/api/v1/projects/{id}/rollbacks',
  entity: 'Plan',
  rootType: 'Plan',
  provides: ['Plan:{id}'],
  invalidates: ['Plan[]'],
  bodyCodec: codec_RollbackInputBody,
  responseCodec: codec_Plan,
} as const satisfies OperationMeta;
