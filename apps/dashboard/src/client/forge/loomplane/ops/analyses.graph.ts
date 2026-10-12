import type { OperationMeta } from '../ops';
import { codec_AnalysisGraph } from '../codecs/AnalysisGraph';

export const op_analyses_graph = {
  method: 'GET',
  path: '/api/v1/analyses/{id}/graph',
  entity: 'AnalysisGraph',
  rootType: 'AnalysisGraph',
  provides: ['AnalysisGraph:{id}'],
  invalidates: [],
  responseCodec: codec_AnalysisGraph,
} as const satisfies OperationMeta;
