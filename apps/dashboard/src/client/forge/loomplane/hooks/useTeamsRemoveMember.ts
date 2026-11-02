import { mutation } from '@forge-go/client-core';
import { op_teams_removeMember } from '../ops/teams.removeMember';
import type { Team } from '../types';

export const useTeamsRemoveMember = /*#__PURE__*/ mutation<Team, Team>(op_teams_removeMember);
