import { query } from '@forge-go/client-core';
import { op_serviceRetirements_list } from '../ops/serviceRetirements.list';
import type { RetirementPage } from '../types';

export const useServiceRetirementsList = /*#__PURE__*/ query<RetirementPage>(op_serviceRetirements_list);
