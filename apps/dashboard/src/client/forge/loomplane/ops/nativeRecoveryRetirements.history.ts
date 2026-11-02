import type { OperationMeta } from '../ops';
import { codec_RetirementHistory } from '../codecs/RetirementHistory';

export const op_nativeRecoveryRetirements_history = {
  method: 'GET',
  path: '/api/v1/native-recovery-retirements/{id}/history',
  provides: [],
  invalidates: [],
  responseCodec: codec_RetirementHistory,
} as const satisfies OperationMeta;
