import { query } from '@forge-go/client-core';
import { op_fabriq_status } from '../ops/fabriq.status';
import type { FabriqloomStatus } from '../types';

export const useFabriqStatus = /*#__PURE__*/ query<FabriqloomStatus>(op_fabriq_status);
