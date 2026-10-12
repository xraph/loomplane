import type { Codec } from '../codec-runtime';
import { codec_GitHubStatus_installations } from './GitHubStatus.installations';

export const codec_GitHubStatus: Codec = {"kind": "object", "fields": {"configured": {"ts": "configured"}, "install_url": {"ts": "installUrl"}, "installations": {"ts": "installations", "codec": () => codec_GitHubStatus_installations}}, "required": ["configured", "installations"]};
