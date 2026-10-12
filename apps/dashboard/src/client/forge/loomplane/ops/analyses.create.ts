import type { OperationMeta } from '../ops';
import { codec_Analysis } from '../codecs/Analysis';
import { codec_StartAnalysisInputBody } from '../codecs/StartAnalysisInputBody';

export const op_analyses_create = {
  method: 'POST',
  path: '/api/v1/projects/{id}/analyses',
  entity: 'Analysis',
  rootType: 'Analysis',
  provides: ['Analysis:{id}'],
  invalidates: ['Analysis[]'],
  bodyCodec: codec_StartAnalysisInputBody,
  responseCodec: codec_Analysis,
} as const satisfies OperationMeta;
