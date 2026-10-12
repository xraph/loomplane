import { query } from '@forge-go/client-core';
import { op_environments_list } from '../ops/environments.list';
import type { EnvironmentList } from '../types';

export const useEnvironmentsList = /*#__PURE__*/ query<EnvironmentList>(op_environments_list);
