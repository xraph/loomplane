import { query } from '@forge-go/client-core';
import { op_rollbacks_target } from '../ops/rollbacks.target';
import type { RollbackTarget } from '../types';

export const useRollbacksTarget = /*#__PURE__*/ query<RollbackTarget>(op_rollbacks_target);
