import { mutation } from '@forge-go/client-core';
import { op_dnsZones_setChallengeCredential } from '../ops/dnsZones.setChallengeCredential';
import type { Zone } from '../types';

export const useDnsZonesSetChallengeCredential = /*#__PURE__*/ mutation<Zone, Zone>(op_dnsZones_setChallengeCredential);
