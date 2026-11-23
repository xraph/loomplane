import { query } from '@forge-go/client-core';
import { op_runnerPools_list } from '../ops/runnerPools.list';
import type { RunnersPoolList } from '../types';

export const useRunnerPoolsList = /*#__PURE__*/ query<RunnersPoolList>(op_runnerPools_list);
