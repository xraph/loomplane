import { mutation } from '@forge-go/client-core';
import { op_teams_removeGrant } from '../ops/teams.removeGrant';
import type { Team } from '../types';

export const useTeamsRemoveGrant = /*#__PURE__*/ mutation<Team, Team>(op_teams_removeGrant);
