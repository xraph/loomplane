import { query } from '@forge-go/client-core';
import { op_runnerPools_get } from '../ops/runnerPools.get';
import type { PoolView } from '../types';

export const useRunnerPoolsGet = /*#__PURE__*/ query<PoolView>(op_runnerPools_get);
