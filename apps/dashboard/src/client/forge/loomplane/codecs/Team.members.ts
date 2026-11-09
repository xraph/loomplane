import type { Codec } from '../codec-runtime';
import { codec_TeamMember } from './TeamMember';

export const codec_Team_members: Codec = {"kind": "array", "items": () => codec_TeamMember};
