import { mutation } from '@forge-go/client-core';
import { op_serviceRetirements_create } from '../ops/serviceRetirements.create';
import type { Retirement } from '../types';

export const useServiceRetirementsCreate = /*#__PURE__*/ mutation<Retirement, Retirement>(op_serviceRetirements_create);
