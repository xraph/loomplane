import { mutation } from '@forge-go/client-core';
import { op_connectionSignIns_complete } from '../ops/connectionSignIns.complete';
import type { Connection } from '../types';

export const useConnectionSignInsComplete = /*#__PURE__*/ mutation<Connection, Connection>(op_connectionSignIns_complete);
