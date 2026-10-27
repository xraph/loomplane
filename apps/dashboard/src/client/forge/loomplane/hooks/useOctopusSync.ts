import { mutation } from '@forge-go/client-core';
import { op_octopus_sync } from '../ops/octopus.sync';

export const useOctopusSync = /*#__PURE__*/ mutation(op_octopus_sync);
