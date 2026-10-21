import type { OperationMeta } from '../ops';
import { codec_RetirementHistory } from '../codecs/RetirementHistory';

export const op_serviceRetirements_history = {
  method: 'GET',
  path: '/api/v1/service-backup-retirements/{id}/history',
  provides: [],
  invalidates: [],
  responseCodec: codec_RetirementHistory,
} as const satisfies OperationMeta;
