import { query } from '@forge-go/client-core';
import { op_projectVariables_list } from '../ops/projectVariables.list';
import type { VariableList } from '../types';

export const useProjectVariablesList = /*#__PURE__*/ query<VariableList>(op_projectVariables_list);
