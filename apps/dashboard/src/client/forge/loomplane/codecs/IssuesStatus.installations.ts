import type { Codec } from '../codec-runtime';
import { codec_InstallationIssues } from './InstallationIssues';

export const codec_IssuesStatus_installations: Codec = {"kind": "array", "items": () => codec_InstallationIssues};
