import { query } from '@forge-go/client-core';
import { op_session_get } from '../ops/session.get';
import type { IdentitySession } from '../types';

export const useSessionGet = /*#__PURE__*/ query<IdentitySession>(op_session_get);
