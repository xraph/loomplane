import { query } from '@forge-go/client-core';
import { op_serviceBackups_get } from '../ops/serviceBackups.get';
import type { Backup } from '../types';

export const useServiceBackupsGet = /*#__PURE__*/ query<Backup>(op_serviceBackups_get);
