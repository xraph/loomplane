import { mutation } from '@forge-go/client-core';
import { op_connectionKeyless_replace } from '../ops/connectionKeyless.replace';
import type { Connection } from '../types';

export const useConnectionKeylessReplace = /*#__PURE__*/ mutation<Connection, Connection>(op_connectionKeyless_replace);
