import { mutation } from '@forge-go/client-core';
import { op_teams_addMember } from '../ops/teams.addMember';
import type { Team } from '../types';

export const useTeamsAddMember = /*#__PURE__*/ mutation<Team, Team>(op_teams_addMember);
