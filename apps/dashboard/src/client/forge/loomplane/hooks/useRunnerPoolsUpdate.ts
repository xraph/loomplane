import { mutation } from '@forge-go/client-core';
import { op_runnerPools_update } from '../ops/runnerPools.update';
import type { PoolView } from '../types';

export const useRunnerPoolsUpdate = /*#__PURE__*/ mutation<PoolView, PoolView>(op_runnerPools_update);
