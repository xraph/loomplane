import type { OperationMeta } from '../ops';
import { codec_Retirement } from '../codecs/Retirement';
import { codec_RetirementRetryInputBody } from '../codecs/RetirementRetryInputBody';

export const op_serviceRetirements_retry = {
  method: 'POST',
  path: '/api/v1/service-backup-retirements/{id}/retry',
  entity: 'Retirement',
  rootType: 'Retirement',
  provides: ['Retirement:{id}'],
  invalidates: ['Backup[]', 'Retirement[]'],
  bodyCodec: codec_RetirementRetryInputBody,
  responseCodec: codec_Retirement,
} as const satisfies OperationMeta;
