import { query } from '@forge-go/client-core';
import { op_octopus_status } from '../ops/octopus.status';
import type { OctopusStatus } from '../types';

export const useOctopusStatus = /*#__PURE__*/ query<OctopusStatus>(op_octopus_status);
