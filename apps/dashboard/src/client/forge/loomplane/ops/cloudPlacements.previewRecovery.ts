import type { OperationMeta } from '../ops';
import { codec_RecoveryInputBody } from '../codecs/RecoveryInputBody';
import { codec_ReservationRecovery } from '../codecs/ReservationRecovery';

export const op_cloudPlacements_previewRecovery = {
  method: 'POST',
  path: '/api/v1/clouds/{cloudId}/placements/preview-recovery',
  provides: [],
  invalidates: [],
  bodyCodec: codec_RecoveryInputBody,
  responseCodec: codec_ReservationRecovery,
} as const satisfies OperationMeta;
