import type { Codec } from '../codec-runtime';
import { codec_OrgInvitation } from './OrgInvitation';

export const codec_CreatedInvitation: Codec = {"kind": "object", "fields": {"accept_path": {"ts": "acceptPath"}, "email_message": {"ts": "emailMessage"}, "email_status": {"ts": "emailStatus"}, "invitation": {"ts": "invitation", "codec": () => codec_OrgInvitation}}, "required": ["accept_path", "email_message", "email_status", "invitation"]};
