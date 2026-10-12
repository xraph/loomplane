import { query } from '@forge-go/client-core';
import { op_analyses_graph } from '../ops/analyses.graph';
import type { AnalysisGraph } from '../types';

export const useAnalysesGraph = /*#__PURE__*/ query<AnalysisGraph>(op_analyses_graph);
