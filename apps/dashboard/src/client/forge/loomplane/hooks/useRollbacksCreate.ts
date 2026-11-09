import { mutation } from '@forge-go/client-core';
import { op_rollbacks_create } from '../ops/rollbacks.create';
import type { Plan } from '../types';

export const useRollbacksCreate = /*#__PURE__*/ mutation<Plan, Plan>(op_rollbacks_create);
