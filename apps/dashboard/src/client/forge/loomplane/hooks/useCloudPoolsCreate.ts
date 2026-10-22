import { mutation } from '@forge-go/client-core';
import { op_cloudPools_create } from '../ops/cloudPools.create';
import type { Pool } from '../types';

export const useCloudPoolsCreate = /*#__PURE__*/ mutation<Pool, Pool>(op_cloudPools_create);
