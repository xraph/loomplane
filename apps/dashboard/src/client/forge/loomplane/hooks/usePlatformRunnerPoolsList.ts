import { query } from '@forge-go/client-core';
import { op_platformRunnerPools_list } from '../ops/platformRunnerPools.list';
import type { RunnersPoolList } from '../types';

export const usePlatformRunnerPoolsList = /*#__PURE__*/ query<RunnersPoolList>(op_platformRunnerPools_list);
