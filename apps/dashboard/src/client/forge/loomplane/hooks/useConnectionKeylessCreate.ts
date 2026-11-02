import { mutation } from '@forge-go/client-core';
import { op_connectionKeyless_create } from '../ops/connectionKeyless.create';
import type { Connection } from '../types';

export const useConnectionKeylessCreate = /*#__PURE__*/ mutation<Connection, Connection>(op_connectionKeyless_create);
