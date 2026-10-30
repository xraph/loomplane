import { query } from '@forge-go/client-core';
import { op_setup_status } from '../ops/setup.status';
import type { SetupStatus } from '../types';

export const useSetupStatus = /*#__PURE__*/ query<SetupStatus>(op_setup_status);
