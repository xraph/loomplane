import type { OperationMeta } from '../ops';
import { codec_AnalysisList } from '../codecs/AnalysisList';

export const op_analyses_list = {
  method: 'GET',
  path: '/api/v1/projects/{id}/analyses',
  entity: 'Analysis',
  rootType: 'AnalysisList',
  provides: ['Analysis:{id}', 'Analysis[]'],
  invalidates: [],
  responseCodec: codec_AnalysisList,
} as const satisfies OperationMeta;
