import { mutation } from '@forge-go/client-core';
import { op_environments_create } from '../ops/environments.create';
import type { Environment } from '../types';

export const useEnvironmentsCreate = /*#__PURE__*/ mutation<Environment, Environment>(op_environments_create);
