import type { Codec } from '../codec-runtime';
import { codec_Project } from './Project';

export const codec_ProjectList_projects: Codec = {"kind": "array", "items": () => codec_Project};
