import type { Codec } from '../codec-runtime';
import { codec_GitLabConnectionView } from './GitLabConnectionView';

export const codec_GitLabStatus_connections: Codec = {"kind": "array", "items": () => codec_GitLabConnectionView};
