import type { Codec } from '../codec-runtime';
import { codec_Team } from './Team';

export const codec_TeamList_teams: Codec = {"kind": "array", "items": () => codec_Team};
