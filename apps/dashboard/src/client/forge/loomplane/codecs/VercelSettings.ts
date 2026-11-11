import type { Codec } from '../codec-runtime';
import { codec_VercelSettings_service_commands } from './VercelSettings.service_commands';

export const codec_VercelSettings: Codec = {"kind": "object", "fields": {"root": {"ts": "root"}, "service_commands": {"ts": "serviceCommands", "codec": () => codec_VercelSettings_service_commands}, "services": {"ts": "services"}}, "required": ["services"]};
