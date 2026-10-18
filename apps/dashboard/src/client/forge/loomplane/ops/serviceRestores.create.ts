import type { OperationMeta } from '../ops';
import { codec_Restore } from '../codecs/Restore';
import { codec_RestoreInputBody } from '../codecs/RestoreInputBody';

export const op_serviceRestores_create = {
  method: 'POST',
  path: '/api/v1/service-backups/{id}/restore',
  entity: 'Restore',
  rootType: 'Restore',
  provides: ['Restore:{id}'],
  invalidates: ['Restore[]'],
  bodyCodec: codec_RestoreInputBody,
  responseCodec: codec_Restore,
} as const satisfies OperationMeta;
