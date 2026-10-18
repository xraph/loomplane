import { mutation } from '@forge-go/client-core';
import { op_serviceBackups_create } from '../ops/serviceBackups.create';
import type { Backup } from '../types';

export const useServiceBackupsCreate = /*#__PURE__*/ mutation<Backup, Backup>(op_serviceBackups_create);
