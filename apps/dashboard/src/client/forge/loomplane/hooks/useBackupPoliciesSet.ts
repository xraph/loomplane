import { mutation } from '@forge-go/client-core';
import { op_backupPolicies_set } from '../ops/backupPolicies.set';
import type { Policy } from '../types';

export const useBackupPoliciesSet = /*#__PURE__*/ mutation<Policy, Policy>(op_backupPolicies_set);
