import type { OperationMeta } from '../ops';
import { codec_RetirementPage } from '../codecs/RetirementPage';

export const op_serviceRetirements_list = {
  method: 'GET',
  path: '/api/v1/projects/{id}/service-backup-retirements',
  entity: 'Retirement',
  rootType: 'RetirementPage',
  provides: ['Retirement:{id}', 'Retirement[]'],
  invalidates: [],
  responseCodec: codec_RetirementPage,
} as const satisfies OperationMeta;
