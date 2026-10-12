import type { Codec } from '../codec-runtime';
import { codec_Workspace_evidence } from './Workspace.evidence';
import { codec_Workspace_members } from './Workspace.members';

export const codec_Workspace: Codec = {"kind": "object", "fields": {"ecosystem": {"ts": "ecosystem"}, "evidence": {"ts": "evidence", "codec": () => codec_Workspace_evidence}, "members": {"ts": "members", "codec": () => codec_Workspace_members}, "path": {"ts": "path"}, "source": {"ts": "source"}, "tool": {"ts": "tool"}}, "required": ["ecosystem", "evidence", "members", "path", "tool"]};
