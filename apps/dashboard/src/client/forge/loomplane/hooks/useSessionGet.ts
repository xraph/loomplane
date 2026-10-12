import { query } from '@forge-go/client-core';
import { op_session_get } from '../ops/session.get';
import type { Session } from '../types';

export const useSessionGet = /*#__PURE__*/ query<Session>(op_session_get);
