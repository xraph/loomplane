import type { OperationMeta } from '../ops';
import { codec_Backup } from '../codecs/Backup';
import { codec_BackupCreateInputBody } from '../codecs/BackupCreateInputBody';

export const op_serviceBackups_create = {
  method: 'POST',
  path: '/api/v1/projects/{id}/service-backups',
  entity: 'Backup',
  rootType: 'Backup',
  provides: ['Backup:{id}'],
  invalidates: ['Backup[]'],
  bodyCodec: codec_BackupCreateInputBody,
  responseCodec: codec_Backup,
} as const satisfies OperationMeta;
