import { mutation } from '@forge-go/client-core';
import { op_variables_create } from '../ops/variables.create';
import type { Variable } from '../types';

export const useVariablesCreate = /*#__PURE__*/ mutation<Variable, Variable>(op_variables_create);
