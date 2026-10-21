import { query } from '@forge-go/client-core';
import { op_cloudPools_list } from '../ops/cloudPools.list';
import type { PoolList } from '../types';

export const useCloudPoolsList = /*#__PURE__*/ query<PoolList>(op_cloudPools_list);
