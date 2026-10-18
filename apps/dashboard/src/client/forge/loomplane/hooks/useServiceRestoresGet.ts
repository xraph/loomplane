import { query } from '@forge-go/client-core';
import { op_serviceRestores_get } from '../ops/serviceRestores.get';
import type { Restore } from '../types';

export const useServiceRestoresGet = /*#__PURE__*/ query<Restore>(op_serviceRestores_get);
