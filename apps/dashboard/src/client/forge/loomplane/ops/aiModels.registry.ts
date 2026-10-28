import type { OperationMeta } from '../ops';
import { codec_RegistryList } from '../codecs/RegistryList';

export const op_aiModels_registry = {
  method: 'GET',
  path: '/api/v1/ai/models/registry',
  entity: 'EntrySummary',
  rootType: 'RegistryList',
  provides: ['EntrySummary:{id}', 'EntrySummary[]'],
  invalidates: [],
  responseCodec: codec_RegistryList,
} as const satisfies OperationMeta;
