import { query } from '@forge-go/client-core';
import { op_cloudCapacity_listExpansions } from '../ops/cloudCapacity.listExpansions';
import type { ExpansionList } from '../types';

export const useCloudCapacityListExpansions = /*#__PURE__*/ query<ExpansionList>(op_cloudCapacity_listExpansions);
