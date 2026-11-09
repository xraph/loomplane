import type { OperationMeta } from '../ops';
import { codec_EntryDetail } from '../codecs/EntryDetail';

export const op_aiModels_entry = {
  method: 'GET',
  path: '/api/v1/ai/models/registry/entry',
  entity: 'EntryDetail',
  rootType: 'EntryDetail',
  provides: ['EntryDetail:{id}'],
  invalidates: [],
  responseCodec: codec_EntryDetail,
} as const satisfies OperationMeta;
