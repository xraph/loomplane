import type { Codec } from '../codec-runtime';
import { codec_TeamList_teams } from './TeamList.teams';

export const codec_TeamList: Codec = {"kind": "object", "fields": {"teams": {"ts": "teams", "codec": () => codec_TeamList_teams}}, "required": ["teams"]};
