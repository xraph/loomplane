import type { OperationMeta } from '../ops';
import { codec_StorageVolumeList } from '../codecs/StorageVolumeList';

export const op_connectionStorage_list = {
  method: 'GET',
  path: '/api/v1/connections/{id}/storage',
  entity: 'StoredVolume',
  rootType: 'StorageVolumeList',
  provides: ['StoredVolume:{id}', 'StoredVolume[]'],
  invalidates: [],
  responseCodec: codec_StorageVolumeList,
} as const satisfies OperationMeta;
