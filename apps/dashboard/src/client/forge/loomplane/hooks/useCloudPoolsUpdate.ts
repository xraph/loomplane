import { mutation } from '@forge-go/client-core';
import { op_cloudPools_update } from '../ops/cloudPools.update';
import type { Pool } from '../types';

export const useCloudPoolsUpdate = /*#__PURE__*/ mutation<Pool, Pool>(op_cloudPools_update);
