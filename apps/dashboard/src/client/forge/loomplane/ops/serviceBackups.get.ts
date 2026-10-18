import type { OperationMeta } from '../ops';
import { codec_Backup } from '../codecs/Backup';

export const op_serviceBackups_get = {
  method: 'GET',
  path: '/api/v1/service-backups/{id}',
  entity: 'Backup',
  rootType: 'Backup',
  provides: ['Backup:{id}'],
  invalidates: [],
  responseCodec: codec_Backup,
} as const satisfies OperationMeta;
