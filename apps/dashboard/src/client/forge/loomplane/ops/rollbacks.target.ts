import type { OperationMeta } from '../ops';
import { codec_RollbackTarget } from '../codecs/RollbackTarget';

export const op_rollbacks_target = {
  method: 'GET',
  path: '/api/v1/projects/{id}/environments/{environment}/rollback-target',
  provides: [],
  invalidates: [],
  responseCodec: codec_RollbackTarget,
} as const satisfies OperationMeta;
