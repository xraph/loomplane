import { query } from '@forge-go/client-core';
import { op_connectionKeyless_setup } from '../ops/connectionKeyless_setup';
import type { KeylessSetup } from '../types';

export const useConnectionKeylessSetup = /*#__PURE__*/ query<KeylessSetup>(op_connectionKeyless_setup);
