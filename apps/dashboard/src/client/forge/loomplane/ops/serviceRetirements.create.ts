import type { OperationMeta } from '../ops';
import { codec_Retirement } from '../codecs/Retirement';
import { codec_RetirementInputBody } from '../codecs/RetirementInputBody';

export const op_serviceRetirements_create = {
  method: 'POST',
  path: '/api/v1/service-backups/{id}/retire',
  entity: 'Retirement',
  rootType: 'Retirement',
  provides: ['Retirement:{id}'],
  invalidates: ['Backup:{id}', 'Backup[]', 'Retirement[]'],
  bodyCodec: codec_RetirementInputBody,
  responseCodec: codec_Retirement,
} as const satisfies OperationMeta;
