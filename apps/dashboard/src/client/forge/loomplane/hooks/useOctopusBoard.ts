import { query } from '@forge-go/client-core';
import { op_octopus_board } from '../ops/octopus.board';
import type { Board } from '../types';

export const useOctopusBoard = /*#__PURE__*/ query<Board>(op_octopus_board);
