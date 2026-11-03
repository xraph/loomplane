import { query } from '@forge-go/client-core';
import { op_teams_list } from '../ops/teams.list';
import type { TeamList } from '../types';

export const useTeamsList = /*#__PURE__*/ query<TeamList>(op_teams_list);
