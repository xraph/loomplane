import type { OperationMeta } from '../ops';
import { codec_Restore } from '../codecs/Restore';

export const op_serviceRestores_get = {
  method: 'GET',
  path: '/api/v1/service-restores/{id}',
  entity: 'Restore',
  rootType: 'Restore',
  provides: ['Restore:{id}'],
  invalidates: [],
  responseCodec: codec_Restore,
} as const satisfies OperationMeta;
