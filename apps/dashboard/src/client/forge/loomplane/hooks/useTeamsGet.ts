import { query } from '@forge-go/client-core';
import { op_teams_get } from '../ops/teams.get';
import type { Team } from '../types';

export const useTeamsGet = /*#__PURE__*/ query<Team>(op_teams_get);
