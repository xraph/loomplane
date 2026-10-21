import { query } from '@forge-go/client-core';
import { op_cloudTargets_list } from '../ops/cloudTargets.list';
import type { CloudTargetList } from '../types';

export const useCloudTargetsList = /*#__PURE__*/ query<CloudTargetList>(op_cloudTargets_list);
