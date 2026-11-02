import { mutation } from '@forge-go/client-core';
import { op_teams_delete } from '../ops/teams.delete';

export const useTeamsDelete = /*#__PURE__*/ mutation(op_teams_delete);
