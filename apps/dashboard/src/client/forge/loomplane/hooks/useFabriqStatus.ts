import { query } from '@forge-go/client-core';
import { op_fabriq_status } from '../ops/fabriq.status';
import type { Status } from '../types';

export const useFabriqStatus = /*#__PURE__*/ query<Status>(op_fabriq_status);
