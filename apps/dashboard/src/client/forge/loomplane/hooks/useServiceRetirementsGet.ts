import { query } from '@forge-go/client-core';
import { op_serviceRetirements_get } from '../ops/serviceRetirements.get';
import type { Retirement } from '../types';

export const useServiceRetirementsGet = /*#__PURE__*/ query<Retirement>(op_serviceRetirements_get);
