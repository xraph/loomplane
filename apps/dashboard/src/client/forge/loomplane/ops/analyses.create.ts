import type { OperationMeta } from '../ops';
import { codec_AnalysisList } from '../codecs/AnalysisList';
import { codec_StartAnalysisInputBody } from '../codecs/StartAnalysisInputBody';

export const op_analyses_create = {
  method: 'POST',
  path: '/api/v1/projects/{id}/analyses',
  entity: 'Analysis',
  rootType: 'AnalysisList',
  provides: ['Analysis:{id}'],
  invalidates: ['Analysis[]'],
  bodyCodec: codec_StartAnalysisInputBody,
  responseCodec: codec_AnalysisList,
} as const satisfies OperationMeta;
