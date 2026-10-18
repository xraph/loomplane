import type { OperationMeta } from '../ops';
import { codec_Backup } from '../codecs/Backup';

export const op_serviceBackups_retry = {
  method: 'POST',
  path: '/api/v1/service-backups/{id}/retry',
  entity: 'Backup',
  rootType: 'Backup',
  provides: ['Backup:{id}'],
  invalidates: ['Backup[]'],
  responseCodec: codec_Backup,
} as const satisfies OperationMeta;
