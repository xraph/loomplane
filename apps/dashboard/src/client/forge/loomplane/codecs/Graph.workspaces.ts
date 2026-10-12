import type { Codec } from '../codec-runtime';
import { codec_Workspace } from './Workspace';

export const codec_Graph_workspaces: Codec = {"kind": "array", "items": () => codec_Workspace};
