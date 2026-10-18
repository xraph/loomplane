import { mutation } from '@forge-go/client-core';
import { op_serviceRestores_create } from '../ops/serviceRestores.create';
import type { Restore } from '../types';

export const useServiceRestoresCreate = /*#__PURE__*/ mutation<Restore, Restore>(op_serviceRestores_create);
