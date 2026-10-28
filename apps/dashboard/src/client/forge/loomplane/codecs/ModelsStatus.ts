import type { Codec } from '../codec-runtime';
import { codec_Cost } from './Cost';
import { codec_EntrySummary } from './EntrySummary';
import { codec_Fit } from './Fit';
import { codec_GPURequest } from './GPURequest';
import { codec_ModelsDeployment } from './ModelsDeployment';
import { codec_ModelsStatus_environments } from './ModelsStatus.environments';
import { codec_ModelsStatus_linked } from './ModelsStatus.linked';
import { codec_PinStatus } from './PinStatus';

export const codec_ModelsStatus: Codec = {"kind": "object", "fields": {"can_edit": {"ts": "canEdit"}, "connection_id": {"ts": "connectionId"}, "connection_name": {"ts": "connectionName"}, "cost": {"ts": "cost", "codec": () => codec_Cost}, "deployment": {"ts": "deployment", "codec": () => codec_ModelsDeployment}, "engine_version": {"ts": "engineVersion"}, "entry": {"ts": "entry", "codec": () => codec_EntrySummary}, "environments": {"ts": "environments", "codec": () => codec_ModelsStatus_environments}, "gpu": {"ts": "gpu", "codec": () => codec_GPURequest}, "hint": {"ts": "hint"}, "internal_url": {"ts": "internalUrl"}, "kind": {"ts": "kind"}, "linked": {"ts": "linked", "codec": () => codec_ModelsStatus_linked}, "max_model_len": {"ts": "maxModelLen"}, "message": {"ts": "message"}, "month_to_date_usd": {"ts": "monthToDateUsd"}, "move_cost": {"ts": "moveCost", "codec": () => codec_Cost}, "pin": {"ts": "pin", "codec": () => codec_PinStatus}, "plan_id": {"ts": "planId"}, "projected_usd": {"ts": "projectedUsd"}, "state": {"ts": "state"}, "update_available": {"ts": "updateAvailable"}, "verification": {"ts": "verification", "codec": () => codec_Fit}}, "required": ["can_edit", "connection_id", "connection_name", "cost", "engine_version", "entry", "environments", "kind", "linked", "max_model_len", "month_to_date_usd", "projected_usd", "state", "update_available", "verification"]};
