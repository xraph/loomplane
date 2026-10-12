import { mutation } from '@forge-go/client-core';
import { op_projectVariables_create } from '../ops/projectVariables.create';
import type { Variable } from '../types';

export const useProjectVariablesCreate = /*#__PURE__*/ mutation<Variable, Variable>(op_projectVariables_create);
