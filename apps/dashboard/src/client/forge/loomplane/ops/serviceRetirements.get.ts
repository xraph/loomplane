import type { OperationMeta } from '../ops';
import { codec_Retirement } from '../codecs/Retirement';

export const op_serviceRetirements_get = {
  method: 'GET',
  path: '/api/v1/service-backup-retirements/{id}',
  entity: 'Retirement',
  rootType: 'Retirement',
  provides: ['Retirement:{id}'],
  invalidates: [],
  responseCodec: codec_Retirement,
} as const satisfies OperationMeta;
