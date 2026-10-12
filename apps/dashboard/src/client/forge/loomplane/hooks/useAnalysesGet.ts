import { query } from '@forge-go/client-core';
import { op_analyses_get } from '../ops/analyses.get';
import type { Analysis } from '../types';

export const useAnalysesGet = /*#__PURE__*/ query<Analysis>(op_analyses_get);
