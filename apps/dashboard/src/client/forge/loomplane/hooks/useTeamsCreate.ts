import { mutation } from '@forge-go/client-core';
import { op_teams_create } from '../ops/teams.create';
import type { Team } from '../types';

export const useTeamsCreate = /*#__PURE__*/ mutation<Team, Team>(op_teams_create);
