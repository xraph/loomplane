import { mutation } from '@forge-go/client-core';
import { op_analyses_create } from '../ops/analyses.create';
import type { Analysis } from '../types';

export const useAnalysesCreate = /*#__PURE__*/ mutation<Analysis, Analysis>(op_analyses_create);
