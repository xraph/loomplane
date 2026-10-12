import { query } from '@forge-go/client-core';
import { op_variables_list } from '../ops/variables.list';
import type { VariableList } from '../types';

export const useVariablesList = /*#__PURE__*/ query<VariableList>(op_variables_list);
