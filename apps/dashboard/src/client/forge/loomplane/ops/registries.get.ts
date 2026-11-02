import type { OperationMeta } from '../ops';
import { codec_ManagedRegistryView } from '../codecs/ManagedRegistryView';

export const op_registries_get = {
  method: 'GET',
  path: '/api/v1/connections/{connectionId}/registry',
  entity: 'ManagedRegistryBackup',
  rootType: 'ManagedRegistryView',
  provides: ['ManagedRegistryBackup:{id}', 'ManagedRegistryBackup[]'],
  invalidates: [],
  responseCodec: codec_ManagedRegistryView,
} as const satisfies OperationMeta;
