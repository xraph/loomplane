import type { OperationMeta } from '../ops';
import { codec_EntryDetail } from '../codecs/EntryDetail';
import { codec_InspectInputBody } from '../codecs/InspectInputBody';

export const op_aiModels_inspect = {
  method: 'POST',
  path: '/api/v1/ai/models/inspect',
  entity: 'EntryDetail',
  rootType: 'EntryDetail',
  provides: ['EntryDetail:{id}'],
  invalidates: ['EntryDetail[]'],
  bodyCodec: codec_InspectInputBody,
  responseCodec: codec_EntryDetail,
} as const satisfies OperationMeta;
