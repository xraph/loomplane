import { mutation } from '@forge-go/client-core';
import { op_serviceBackups_retry } from '../ops/serviceBackups.retry';
import type { Backup } from '../types';

export const useServiceBackupsRetry = /*#__PURE__*/ mutation<Backup, Backup>(op_serviceBackups_retry);
