import { query } from '@forge-go/client-core';
import { op_variables_resolve } from '../ops/variables.resolve';
import type { Resolution } from '../types';

export const useVariablesResolve = /*#__PURE__*/ query<Resolution>(op_variables_resolve);
