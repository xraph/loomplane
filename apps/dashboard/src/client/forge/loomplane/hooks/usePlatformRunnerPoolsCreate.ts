import { mutation } from '@forge-go/client-core';
import { op_platformRunnerPools_create } from '../ops/platformRunnerPools.create';
import type { PoolView } from '../types';

export const usePlatformRunnerPoolsCreate = /*#__PURE__*/ mutation<PoolView, PoolView>(op_platformRunnerPools_create);
