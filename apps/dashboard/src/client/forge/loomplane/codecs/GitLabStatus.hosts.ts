import type { Codec } from '../codec-runtime';
import { codec_GitLabHostView } from './GitLabHostView';

export const codec_GitLabStatus_hosts: Codec = {"kind": "array", "items": () => codec_GitLabHostView};
