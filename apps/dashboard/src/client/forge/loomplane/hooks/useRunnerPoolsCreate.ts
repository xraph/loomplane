import { mutation } from '@forge-go/client-core';
import { op_runnerPools_create } from '../ops/runnerPools.create';
import type { PoolView } from '../types';

export const useRunnerPoolsCreate = /*#__PURE__*/ mutation<PoolView, PoolView>(op_runnerPools_create);
