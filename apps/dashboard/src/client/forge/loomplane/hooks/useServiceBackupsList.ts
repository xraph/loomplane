import { query } from '@forge-go/client-core';
import { op_serviceBackups_list } from '../ops/serviceBackups.list';
import type { BackupList } from '../types';

export const useServiceBackupsList = /*#__PURE__*/ query<BackupList>(op_serviceBackups_list);
