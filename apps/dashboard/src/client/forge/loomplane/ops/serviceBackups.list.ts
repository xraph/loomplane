import type { OperationMeta } from '../ops';
import { codec_BackupList } from '../codecs/BackupList';

export const op_serviceBackups_list = {
  method: 'GET',
  path: '/api/v1/projects/{id}/service-backups',
  entity: 'Backup',
  rootType: 'BackupList',
  provides: ['Backup:{id}', 'Backup[]'],
  invalidates: [],
  responseCodec: codec_BackupList,
} as const satisfies OperationMeta;
