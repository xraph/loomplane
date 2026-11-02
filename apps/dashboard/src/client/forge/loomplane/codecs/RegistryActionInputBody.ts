import type { Codec } from '../codec-runtime';

export const codec_RegistryActionInputBody: Codec = {"kind": "object", "fields": {"backup_id": {"ts": "backupId"}, "delete_data": {"ts": "deleteData"}, "generation": {"ts": "generation"}, "hostname": {"ts": "hostname"}, "kind": {"ts": "kind"}, "remove_setup": {"ts": "removeSetup"}}, "required": ["generation", "hostname", "kind"]};
