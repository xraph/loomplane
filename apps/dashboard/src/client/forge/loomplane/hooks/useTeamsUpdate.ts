import { mutation } from '@forge-go/client-core';
import { op_teams_update } from '../ops/teams.update';
import type { Team } from '../types';

export const useTeamsUpdate = /*#__PURE__*/ mutation<Team, Team>(op_teams_update);
