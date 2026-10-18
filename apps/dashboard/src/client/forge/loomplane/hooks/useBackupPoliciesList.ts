import { query } from '@forge-go/client-core';
import { op_backupPolicies_list } from '../ops/backupPolicies.list';
import type { PolicyList } from '../types';

export const useBackupPoliciesList = /*#__PURE__*/ query<PolicyList>(op_backupPolicies_list);
