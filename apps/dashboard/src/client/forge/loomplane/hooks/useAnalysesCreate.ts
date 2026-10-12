import { mutation } from '@forge-go/client-core';
import { op_analyses_create } from '../ops/analyses.create';
import type { Analysis, AnalysisList } from '../types';

export const useAnalysesCreate = /*#__PURE__*/ mutation<AnalysisList, Analysis>(op_analyses_create);
