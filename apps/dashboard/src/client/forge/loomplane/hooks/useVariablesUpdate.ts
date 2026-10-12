import { mutation } from '@forge-go/client-core';
import { op_variables_update } from '../ops/variables.update';
import type { Variable } from '../types';

export const useVariablesUpdate = /*#__PURE__*/ mutation<Variable, Variable>(op_variables_update);
