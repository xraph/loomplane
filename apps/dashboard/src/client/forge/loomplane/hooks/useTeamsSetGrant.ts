import { mutation } from '@forge-go/client-core';
import { op_teams_setGrant } from '../ops/teams.setGrant';
import type { Team } from '../types';

export const useTeamsSetGrant = /*#__PURE__*/ mutation<Team, Team>(op_teams_setGrant);
