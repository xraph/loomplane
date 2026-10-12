import type { Codec } from '../codec-runtime';
import { codec_GitHubInstallation } from './GitHubInstallation';

export const codec_GitHubStatus_installations: Codec = {"kind": "array", "items": () => codec_GitHubInstallation};
