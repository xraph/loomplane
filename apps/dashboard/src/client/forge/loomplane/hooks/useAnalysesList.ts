import { query } from '@forge-go/client-core';
import { op_analyses_list } from '../ops/analyses.list';
import type { AnalysisList } from '../types';

export const useAnalysesList = /*#__PURE__*/ query<AnalysisList>(op_analyses_list);
