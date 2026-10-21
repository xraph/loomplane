import { mutation } from '@forge-go/client-core';
import { op_clouds_update } from '../ops/clouds.update';
import type { Profile } from '../types';

export const useCloudsUpdate = /*#__PURE__*/ mutation<Profile, Profile>(op_clouds_update);
