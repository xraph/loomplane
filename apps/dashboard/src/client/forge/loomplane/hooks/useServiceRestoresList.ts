import { query } from '@forge-go/client-core';
import { op_serviceRestores_list } from '../ops/serviceRestores.list';
import type { RestoreList } from '../types';

export const useServiceRestoresList = /*#__PURE__*/ query<RestoreList>(op_serviceRestores_list);
