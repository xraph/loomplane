// Generated types

export enum ConnectionState {
  DISCONNECTED = 'disconnected',
  CONNECTING = 'connecting',
  CONNECTED = 'connected',
  RECONNECTING = 'reconnecting',
  CLOSED = 'closed',
  ERROR = 'error',
}

export interface APIKey {
  access: string;
  createdAt: string;
  createdBy: string;
  expiresAt?: string;
  hint: string;
  id: string;
  lastUsedAt?: string;
  name: string;
  revokedAt?: string;
  state: string;
}

export interface APIKeyList {
  keys: APIKey[];
}

export interface AccountSpend {
  cloud: string;
  connectionId: string;
  currency: string;
  days: Point[];
  name: string;
  total: number;
}

export interface Action {
  adoptCloudPlacement?: boolean;
  changes?: Change[];
  cloudAdoption?: CloudAdoption;
  compensation?: CloudCompensation;
  connectionId?: string;
  creates: string;
  dependsOn?: string[];
  engine: string;
  id: string;
  name: string;
  op: string;
  provider: string;
  reason: string;
  region?: string;
  resource?: ResourceConfig;
  routes?: CloudinfraRoute[];
  service?: ServiceConfig;
  step: number;
  subject: string;
}

export interface ActivityDay {
  date: string;
  failed: number;
  succeeded: number;
}

export interface ActivitySummary {
  analyses: AnalysisCounts;
  daily: ActivityDay[];
  days: number;
  deployments: DeploymentCounts;
  drift: DriftCounts;
  plans: PlanCounts;
  since: string;
}

export interface AddSourceInputBody {
  alias?: string;
  ref?: string;
  repository: string;
  root?: string;
}

export interface Amount {
  currency: string;
  days: Point[];
  total: number;
  undated: number;
}

export interface Analysis {
  commitSha: string;
  createdAt: string;
  createdBy: string;
  error: string;
  finishedAt?: string;
  id: string;
  orgId: string;
  projectId: string;
  ref: string;
  repository: string;
  source: string;
  sourceId: string;
  startedAt?: string;
  status: string;
  summary: GraphSummary;
}

export interface AnalysisCounts {
  failed: number;
  succeeded: number;
}

export interface AnalysisGraph {
  graph: Graph;
  id: string;
}

export interface AnalysisList {
  analyses: Analysis[];
}

export interface AppliedInputs {
  artifactKey?: string;
  source?: PlanSource;
  variables?: string;
}

export interface ApplyCloudInputBody {
  idempotencyKey: string;
  previewId: string;
  revision: string;
}

export interface ApplyInputBody {
  review: string;
}

export interface Assignment {
  cloudId: string;
  cloudRevision: number;
  defaultPlacement?: DefaultPlacement;
  environmentId: string;
  grantRevision: number;
  orgId: string;
  projectId: string;
  revision: number;
  targets: Target[];
}

export interface Backup {
  attempt: number;
  bytes: number;
  completedAt?: string;
  createdAt: string;
  createdBy: string;
  environmentId: string;
  error?: string;
  id: string;
  name: string;
  orgId: string;
  projectId: string;
  restoredAt?: string;
  sha256?: string;
  status: string;
}

export interface BackupCreateInputBody {
  environment: string;
  requestId: string;
  resource: string;
}

export interface BackupList {
  backups: Backup[];
}

export interface Binding {
  commitSha: string;
  config: string;
  connectionId: string;
  createdAt: string;
  drift: string;
  driftMessage: string;
  engine: string;
  environmentId: string;
  externalId: string;
  id: string;
  name: string;
  observedAt?: string;
  orgId: string;
  physicalId?: string;
  planId: string;
  projectId: string;
  provider: string;
  region: string;
  revision: string;
  subject: string;
  updatedAt: string;
  url: string;
}

export interface BindingList {
  bindings: Binding[];
}

export interface Build {
  builder?: string;
  context?: string;
  image?: string;
  registry?: string;
}

export interface BuildSystem {
  evidence: Evidence;
  name: string;
  path: string;
  source?: string;
}

export interface BuildTarget {
  builder?: string;
  builderSettings?: Record<string, any>;
  image?: string;
  registry?: string;
  registrySettings?: Record<string, any>;
}

export interface CatalogKindList {
  kinds: KindInfo[];
}

export interface CatalogconnectCreateInputBody {
  dsn: string;
  engine: string;
  name: string;
}

export interface CatalogconnectEndpoint {
  createdAt: string;
  createdBy: string;
  database?: string;
  engine: string;
  host: string;
  id: string;
  name: string;
  orgId: string;
  port: number;
  revision: number;
  tls: string;
  username?: string;
}

export interface CatalogproxySession {
  expiresAt: string;
  url: string;
}

export interface Change {
  field: string;
  from: string;
  to: string;
}

export interface ChangeInput {
  cloudId: string;
  connectionId?: string;
  default?: boolean;
  environmentId?: string;
  poolId?: string;
  provider?: string;
  region?: string;
  revision: number;
  runtime?: string;
  targetId?: string;
}

export interface ChangeInputBody {
  cloudId: string;
  connectionId?: string;
  default?: boolean;
  environmentId?: string;
  poolId?: string;
  provider?: string;
  region?: string;
  revision: number;
  runtime?: string;
  targetId?: string;
}

export interface CheckInputBody {
  dependency: string;
  dsn: string;
  engine: string;
  kind: string;
  target: string;
}

export interface CheckResult {
  missing?: string[];
  notes?: string[];
  ok: boolean;
  problems?: string[];
  version?: string;
}

export interface ChooseConnectionInputBody {
  connectionId: string;
}

export interface CloudAdoption {
  bindingId: string;
  configDigest: string;
  externalId: string;
  planId: string;
  revision: string;
}

export interface CloudCompensation {
  actionId: string;
  candidate: Binding;
  originalCompleted: boolean;
  planId: string;
  retained: boolean;
  routes?: CloudinfraRoute[];
  serverAttempt: boolean;
  source: Binding;
}

export interface CloudForm {
  builtin: boolean;
  cloud: string;
  credential?: string;
  help?: string;
  settings?: Setting[];
  title: string;
}

export interface CloudPlacement {
  accountIdentity: string;
  allocation?: string;
  assignmentRevision: number;
  cloudId: string;
  cloudRevision: number;
  connectionGeneration?: number;
  connectionId: string;
  cpuMillis?: number;
  default?: boolean;
  grantRevision?: number;
  memoryBytes?: number;
  poolId?: string;
  poolRevision?: number;
  provider: string;
  region?: string;
  regionSource?: string;
  serverId?: string;
  serverRevision?: number;
  targetId: string;
  targetName: string;
  targetRevision: number;
}

export interface CloudProjectGrantInputBody {
  targetIds: string[];
}

export interface CloudProjectGrantList {
  grants: (ProjectGrant | null)[];
}

export interface CloudSyncEvent {
  attempt: number;
  cloudId: string;
  environments: EnvironmentChange[];
  id: string;
  orgId: string;
  projectId: string;
  reverts?: string;
  status: string;
  targetId: string;
  updatedAt: string;
}

export interface CloudSyncList {
  next?: string;
  syncs: (Sync | null)[];
}

export interface CloudTargetList {
  targets: (Target | null)[];
}

export interface CloudinfraRoute {
  binding: RouteBinding;
  id: string;
  input: RouteInput;
  operationId: string;
  orgId: string;
  projectId: string;
  receipt?: DNSRecord;
  revision: number;
  status: string;
  updatedAt: string;
  verification: RouteVerification;
}

export interface CloudprofilesCreateInputBody {
  defaultPlacement?: {
  connectionId: string;
  provider: string;
  region: string;
};
  name: string;
}

export interface Collection {
  actionId: string;
  attempt: string;
  deploymentId: string;
  id: string;
  message: string;
  operationId: string;
  orgId: string;
  projectId: string;
  state: string;
  updatedAt: string;
}

export interface Commands {
  build?: string;
  dev?: string;
  start?: string;
}

export interface CompleteCloudRoutingInputBody {
  environmentId: string;
  evidence: string;
}

export interface ComponentConfig {
  component: ResolvedComponent;
  dependencies?: Record<string, any>;
  kind: string;
  protection: string;
  resource: string;
  revision: string;
}

export interface ComponentInfo {
  name: string;
  protection: string;
  role: string;
  stateful: boolean;
}

export interface CompositionConfig {
  dependencies?: Record<string, any>;
  kind: string;
  outputs?: Output[];
  revision: string;
  secrets?: GeneratedSecret[];
  version: string;
}

export interface ConnectGitHubInputBody {
  code: string;
  installationId: number;
  state: string;
}

export interface ConnectedRepository {
  defaultBranch: string;
  fullName: string;
  installation: string;
  private: boolean;
  url: string;
}

export interface ConnectedRepositoryList {
  repositories: ConnectedRepository[];
}

export interface Connection {
  account: string;
  accountIdentity?: string;
  checkedAt?: string;
  cloud: string;
  createdAt: string;
  createdBy: string;
  details: Record<string, any>;
  generation: number;
  id: string;
  message: string;
  missing: string[];
  name: string;
  orgId: string;
  status: string;
  updatedAt: string;
}

export interface ConnectionList {
  connections: Connection[];
}

export interface ConnectionsCloudList {
  clouds: CloudForm[];
}

export interface ConnectionsCreateInputBody {
  cloud: string;
  credential: string;
  name: string;
  settings?: Record<string, any>;
}

export interface ConsumerOption {
  name: string;
  pattern?: string;
  required?: boolean;
  title: string;
}

export interface CreateEnvironmentInputBody {
  kind?: string;
  name: string;
}

export interface CreateInvitationInputBody {
  email: string;
  role?: string;
}

export interface CreatePlanInputBody {
  catalog?: boolean;
  environment: string;
  scope?: string[];
}

export interface CreateProjectInputBody {
  color?: string;
  description?: string;
  icon?: string;
  name: string;
  repositories?: {
  alias?: string;
  ref?: string;
  repository: string;
  root?: string;
}[];
  repositoryUrl?: string;
  slug?: string;
}

export interface CreateTargetInputBody {
  connectionId: string;
  displayName: string;
  name: string;
  poolId?: string;
  provider: string;
  region: string;
  runtime: string;
  tags?: string[];
}

export interface CreateVariableInputBody {
  environment?: string;
  key: string;
  secret?: boolean;
  service?: string;
  value: string;
}

export interface CreatedInvitation {
  acceptPath: string;
  emailMessage: string;
  emailStatus: string;
  invitation: OrgInvitation;
}

export interface CreatedKey {
  key: APIKey;
  secret: string;
}

export interface DNSRecord {
  id: string;
  name: string;
  owner?: string;
  ttl: number;
  type: string;
  value: string;
  zoneId: string;
}

export interface DNSZone {
  accountId: string;
  id: string;
  name: string;
  nameservers?: string[];
  verified: boolean;
}

export interface DecideInputBody {
  note?: string;
  revision?: string;
}

export interface DefaultPlacement {
  connectionId: string;
  provider: string;
  region: string;
}

export interface Defaults {
  cloudTags?: string[];
  provider?: string;
  region?: string;
}

export interface Delivery {
  attempts: number;
  createdAt: string;
  deliveredAt?: string;
  eventType: string;
  id: string;
  nextRetryAt?: string;
  status: string;
  statusCode: number;
}

export interface DeliveryList {
  items: Delivery[];
}

export interface DependencyInfo {
  default: boolean;
  external: boolean;
  kinds: string[];
  name: string;
  optional: boolean;
  title: string;
}

export interface DeployedList {
  sources: DeployedRepository[];
}

export interface DeployedRepository {
  alias: string;
  commitSha: string;
  deployedAt: string;
  planId: string;
  repository: string;
  sourceId: string;
}

export interface Deployment {
  commitSha: string;
  environment: string;
  environmentId: string;
  error: string;
  finishedAt?: string;
  id: string;
  orgId: string;
  planId: string;
  projectId: string;
  retries: number;
  sources: SourceArtifact[];
  startedAt: string;
  startedBy: string;
  status: string;
  steps: Step[];
}

export interface DeploymentCounts {
  failed: number;
  medianSeconds: number;
  retried: number;
  running: number;
  succeeded: number;
}

export interface DeploymentList {
  deployments: Deployment[];
  next?: string;
}

export interface DomainOperationList {
  operations: (RouteOperation | null)[];
}

export interface DomainRouteList {
  routes: (CloudinfraRoute | null)[];
}

export interface DrainReview {
  reservations: number;
  review: string;
  serverId: string;
  serverRevision: number;
  usage?: ServerUsage;
}

export interface DriftCounts {
  changed: number;
  deployed: number;
  missing: number;
}

export interface Edge {
  confidence: string;
  evidence: Evidence[];
  from: string;
  kind: string;
  to: string;
}

export interface EditInputBody {
  addResource?: {
  kind: string;
  name: string;
  provider?: string;
  target?: string;
  uses?: Record<string, any>;
  version?: string;
};
  ignore?: string;
  link?: {
  need: string;
  options?: Record<string, any>;
  service: string;
};
  placement?: {
  build?: Build;
  cloudTags?: string[];
  cloudTarget?: string;
  enabled?: boolean;
  endpoint?: ManifestEndpoint;
  environment?: string;
  healthPath?: string;
  name: string;
  provider?: string;
  region?: string;
  section: string;
  target?: string;
};
  setUse?: {
  dependency: string;
  resource: string;
  use: Use;
};
  unlink?: {
  need: string;
  options?: Record<string, any>;
  service: string;
};
}

export interface EndpointList {
  endpoints: (CatalogconnectEndpoint | null)[];
}

export interface EnrollmentBody {
  hostKey: string;
}

export interface EnvLink {
  address?: string;
  field?: string;
  from: string;
  key: string;
  options?: Record<string, any>;
  subject: string;
}

export interface EnvVar {
  from?: Ref;
  name: string;
  requires?: string;
  secret?: string;
  template?: string;
  value?: string;
}

export interface Environment {
  createdAt: string;
  id: string;
  kind: string;
  name: string;
  orgId: string;
  projectId: string;
  updatedAt: string;
}

export interface EnvironmentChange {
  compensationMessage?: string;
  compensationMode?: string;
  deploymentId?: string;
  environment: string;
  environmentId: string;
  error?: string;
  externalRouting: boolean;
  planId?: string;
  projectId?: string;
  routes?: SyncRoute[];
  routingEvidence?: string;
  routingStatus?: string;
  services: string[];
  status: string;
  summary: PlanSummary;
}

export interface EnvironmentCloud {
  assignment: Assignment | null;
  revision: number;
}

export interface EnvironmentCloudEvent {
  cloudId: string | null;
  environmentId: string;
  orgId: string;
  projectId: string;
  revision: number;
}

export interface EnvironmentCloudInputBody {
  cloudId: string;
  revision: number;
}

export interface EnvironmentConnections {
  connections: Record<string, any>;
  environment: string;
}

export interface EnvironmentList {
  environments: Environment[];
}

export interface Evidence {
  detail: string;
  line?: number;
  path: string;
  source?: string;
}

export interface Federation {
  accessGrants?: Record<string, any>;
  destinations?: Record<string, any>;
  networkLinks?: Record<string, any>;
  placements?: Record<string, any>;
  serviceExports?: Record<string, any>;
}

export interface File {
  mode?: number;
  path: string;
}

export interface Finding {
  code: string;
  evidence?: Evidence[];
  message: string;
  name?: string;
  path?: string;
  subject?: string;
}

export interface GatewayRoute {
  env: string;
  path: string;
  stripPrefix?: boolean;
  to: string;
}

export interface GeneratedSecret {
  bytes?: number;
  encoding?: string;
  name: string;
  prefix?: string;
}

export interface GitHubInstallation {
  account: string;
  accountType: string;
  avatarUrl: string;
  createdAt: string;
  id: string;
  manageUrl: string;
}

export interface GitHubStatus {
  configured: boolean;
  installUrl?: string;
  installations: GitHubInstallation[];
}

export interface Graph {
  buildSystems: BuildSystem[];
  edges: Edge[];
  notes: Note[];
  packages: Package[];
  resources: Resource[];
  services: Service[];
  sources?: SourceRef[];
  version: number;
  workspaces: Workspace[];
}

export interface GraphSummary {
  deployable: number;
  needsConfirmation: number;
  packages: number;
  resources: number;
  services: number;
}

export interface HTTPProbe {
  path: string;
  port: string;
}

export interface HandoffInputBody {
  destinationCloudId: string;
  revision: number;
}

export interface HandoffReleaseInputBody {
  drainSeconds: number;
}

export interface Held {
  sameAddress?: string;
}

export interface IdentitySession {
  orgId: string;
  permissions: string[];
  userId: string;
}

export interface Ignore {
  resources?: string[];
}

export interface InfrastructureAction {
  id: string;
  kind: string;
  resourceId: string;
  status: string;
}

export interface InfrastructureStatus {
  engine: string;
  providers: ProviderStatus[];
  store: string;
}

export interface KindInfo {
  components?: ComponentInfo[];
  consumerOptions?: ConsumerOption[];
  dependencies?: DependencyInfo[];
  description?: string;
  engine: boolean;
  name: string;
  outputs?: OutputInfo[];
  placement: string;
  targets: string[];
  title: string;
  versions: string[];
}

export interface KubernetesRule {
  apiGroups: string[];
  resources: string[];
  verbs: string[];
}

export interface Log {
  actionId: string;
  attempt: string;
  deploymentId: string;
  id: string;
  level: string;
  message: string;
  operationId: string;
  orgId: string;
  projectId: string;
  providerId: string;
  source: string;
  stream: string;
  time: string;
  truncated: boolean;
}

export interface LogPage {
  gap: boolean;
  logs: Log[];
  next: string;
}

export interface ManagedDatabaseConfig {
  accountIdentity: string;
  connectionId: string;
  profile: ManagedDatabaseProfile;
}

export interface ManagedDatabaseProfile {
  access: string;
  availability: string;
  backupRetentionUnit: string;
  backupRetentionValue: number;
  computeClass: string;
  finalBackup: string;
  finalBackupRetentionDays: number;
  id: string;
  kind: string;
  pointInTimeRecoveryDays: number;
  provider: string;
  retainAutomatedBackups: boolean;
  revision: string;
  storageEncrypted: boolean;
  storageGb: number;
  storageType: string;
  version: string;
}

export interface Manifest {
  defaults?: Defaults;
  federation?: Federation;
  gateways?: Record<string, any>;
  held?: Held;
  ignore?: Ignore;
  links?: Record<string, any>;
  resources?: Record<string, any>;
  services?: Record<string, any>;
  version: number;
}

export interface ManifestDraft {
  manifest?: Manifest;
  projectId: string;
  sources: SourceRef[];
  yaml: string;
}

export interface ManifestEndpoint {
  hostPort?: number;
  hostname?: string;
  ingressClass?: string;
  tlsSecret?: string;
}

export interface MoveInput {
  destinationServerId: string;
  environmentId: string;
  poolId: string;
  projectId: string;
  review?: string;
  service: string;
}

export interface MoveInputBody {
  destinationServerId: string;
  environmentId: string;
  poolId: string;
  projectId: string;
  review?: string;
  service: string;
}

export interface MoveReview {
  input: MoveInput;
  placement: Placement;
  review: string;
  sourceServerId: string;
}

export interface NativeCopyCapacity {
  availability: string;
  computeClass: string;
  iops?: number;
  maxStorageGb?: number;
  profileId: string;
  profileRevision: string;
  storageGb: number;
  storageType: string;
  throughputMbps?: number;
}

export interface NativeCopyCreateInputBody {
  acknowledgeCharges: boolean;
  requestId: string;
  reviewToken: string;
}

export interface NativeCopyCredentials {
  checkedAt: string;
  connectionName: string;
  copyId: string;
  database: string;
  expiresAt: string;
  password: string;
  provider: string;
  username: string;
}

export interface NativeCopyReview {
  capacity: NativeCopyCapacity;
  notice: string;
  pointId: string;
  requestId: string;
  restoreTime?: string;
  reviewToken: string;
  sourceId: string;
  target: NativeRestoreTarget;
  ttlHours: number;
  validUntil: string;
}

export interface NativeCopyReviewInputBody {
  cloudId: string;
  requestId: string;
  restoreTime?: string;
  targetId: string;
  ttlHours: number;
}

export interface NativeInventory {
  nextBefore?: string;
  points: NativePoint[];
  source: NativeSource;
}

export interface NativePITRBounds {
  earliest: string;
  latest: string;
  protocol: number;
}

export interface NativePITRWindow {
  available: boolean;
  earliest?: string;
  latest?: string;
  observedAt?: string;
  pointId?: string;
  provider: string;
  sourceId: string;
  unavailableReason?: string;
}

export interface NativePoint {
  createdAt: string;
  encrypted: boolean;
  engine: string;
  expiresAt: string | null;
  id: string;
  kind: string;
  kmsKey?: string;
  lastSeenAt: string;
  name: string;
  pitr?: NativePITRBounds;
  present: boolean;
  providerId: string;
  restoreTime?: string;
  sourceId: string;
  status: string;
  version: string;
}

export interface NativeRecoveryCopies {
  copies: NativeRestore[];
  nextBefore?: string;
}

export interface NativeRecoverySources {
  nextBefore?: string;
  sources: NativeSource[];
}

export interface NativeRestore {
  cleanupBy?: string;
  cleanupRequestedAt?: string;
  createdAt: string;
  createdBy: string;
  durationMs: number;
  environmentId: string;
  error?: string;
  expiresAt: string;
  externalId?: string;
  id: string;
  orgId: string;
  physicalId?: string;
  pointId: string;
  projectId: string;
  restoreTime?: string;
  sourceId: string;
  status: string;
  target: NativeRestoreTarget;
  verifiedAt?: string;
}

export interface NativeRestoreTarget {
  accountIdentity: string;
  cloudId?: string;
  cloudRevision?: number;
  connectionId: string;
  grantRevision?: number;
  nativeAccount?: string;
  nativeProject?: string;
  provider: string;
  rdsNetwork?: RDSRecoveryNetwork;
  region: string;
  targetId?: string;
  targetRevision?: number;
}

export interface NativeRetentionPolicy {
  authorizedBy: string;
  createdAt: string;
  createdBy: string;
  enabled: boolean;
  id: string;
  lastError?: string;
  lastRetirementId?: string;
  nextAt: string;
  orgId: string;
  policy: RetentionInput;
  projectId: string;
  revision: number;
  sourceId: string;
  updatedAt: string;
}

export interface NativeRetentionPolicyState {
  configured: boolean;
  policy?: NativeRetentionPolicy;
}

export interface NativeRetirement {
  completedAt?: string;
  createdAt: string;
  createdBy: string;
  error?: string;
  id: string;
  orgId: string;
  pointId: string;
  policy: RetentionInput;
  policyId?: string;
  policyRevision?: number;
  projectId: string;
  sourceId: string;
  status: string;
  witnessId: string;
}

export interface NativeRetirementInputBody {
  acknowledgeDeletion: boolean;
  policy: {
  keepLast: number;
  olderThanDays: number;
};
  requestId: string;
}

export interface NativeRetirementPage {
  nextBefore?: string;
  retirements: NativeRetirement[];
}

export interface NativeSource {
  capturedAt: string;
  checkedAt?: string;
  complete: boolean;
  connectionId: string;
  environmentId: string;
  error?: string;
  id: string;
  name: string;
  observedAt?: string;
  orgId: string;
  physicalId: string;
  projectId: string;
  provider: string;
  region: string;
  state: string;
}

export interface NewKeyBody {
  access: string;
  expiresInDays?: number;
  name: string;
}

export interface Note {
  message: string;
  path?: string;
  source?: string;
}

export interface Notification {
  actionUrl: string;
  body: string;
  createdAt: string;
  emailStatus: string;
  id: string;
  projectId: string;
  read: boolean;
  status: string;
  title: string;
}

export interface NotificationList {
  items: Notification[];
  mailEnabled: boolean;
}

export interface OK {
  ok: boolean;
}

export interface Operation {
  accountId: string;
  actions: InfrastructureAction[];
  attempt: number;
  attempted: boolean;
  bootstrapExpiresAt: string;
  cloudId: string;
  connectionGeneration?: number;
  connectionId: string;
  connectionRevision: string;
  createdAt: string;
  enrolled: boolean;
  estimatedHourlyUsd: number;
  expiresAt: string;
  hostKey?: string;
  id: string;
  input: ServerInput;
  message: string;
  orgId: string;
  ownership: string;
  provider: string;
  publicIpv4: string[];
  resourceId: string;
  review: string;
  serverId: string;
  stateRevision: number;
  status: string;
  updatedAt: string;
}

export interface OperationList {
  operations: (Operation | null)[];
}

export interface OrgInvitation {
  createdAt: string;
  email: string;
  expiresAt: string;
  id: string;
  invitedBy: string;
  role: string;
}

export interface OrgInvitationList {
  invitations: OrgInvitation[];
}

export interface OrgMember {
  email: string;
  id: string;
  joinedAt: string;
  memberRole: string;
  name: string;
  role: string;
  userId: string;
}

export interface OrgMemberList {
  members: OrgMember[];
}

export interface Output {
  envKey: string;
  from?: Ref;
  name: string;
  perConsumer?: boolean;
  requires?: string;
  secret?: boolean;
  template?: string;
}

export interface OutputInfo {
  envKey: string;
  name: string;
  perConsumer: boolean;
}

export interface Package {
  ecosystem: string;
  evidence: Evidence[];
  id: string;
  name: string;
  path: string;
  requires?: Requirement[];
  runtime?: string;
  source?: string;
}

export interface Placement {
  allocation?: string;
  connectionGeneration: number;
  connectionId: string;
  cpuMillis: number;
  memoryBytes: number;
  poolId: string;
  poolRevision: number;
  serverId: string;
  serverRevision: number;
}

export interface Plan {
  actions: Action[];
  catalog: boolean;
  cloudSyncId?: string;
  connections: Record<string, any>;
  createdAt: string;
  createdBy: string;
  decidedAt?: string;
  decidedBy: string;
  decisionNote: string;
  environment: string;
  environmentId: string;
  error: string;
  executable: boolean;
  expiresAt?: string;
  id: string;
  manifestId: string;
  manifestRevision: number;
  orgId: string;
  plannedAt?: string;
  problems: Finding[];
  projectId: string;
  revision: string;
  sources: PlanSource[];
  status: string;
  summary: PlanningSummary;
  warnings: Finding[];
}

export interface PlanCounts {
  approved: number;
  expired: number;
  failed: number;
  rejected: number;
  superseded: number;
  waiting: number;
}

export interface PlanList {
  next?: string;
  plans: Plan[];
}

export interface PlanSource {
  alias: string;
  analysisId: string;
  commitSha: string;
  inScope: boolean;
  ref?: string;
  repository: string;
  root: string;
  sourceId: string;
}

export interface PlanSummary {
  adopt: number;
  create: number;
  delete: number;
  keep: number;
  replace: number;
  update: number;
}

export interface PlanningCloudList {
  clouds: (Profile | null)[];
}

export interface PlanningSummary {
  create: number;
  delete: number;
  keep: number;
  replace: number;
  update: number;
}

export interface Point {
  t: string;
  v: number;
}

export interface Policy {
  createdBy: string;
  enabled: boolean;
  environment: string;
  environmentId: string;
  everyHours: number;
  id: string;
  lastError?: string;
  nextAt: string;
  orgId: string;
  projectId: string;
  resource: string;
}

export interface PolicyInputBody {
  enabled: boolean;
  environment: string;
  everyHours: number;
  resource: string;
}

export interface PolicyList {
  policies: Policy[];
}

export interface Pool {
  cloudId: string;
  id: string;
  members: PoolMember[];
  name: string;
  orgId: string;
  revision: number;
}

export interface PoolInputBody {
  members: {
  cpuMillis: number;
  draining: boolean;
  memoryBytes: number;
  serverId: string;
}[];
  name: string;
  revision: number;
}

export interface PoolList {
  pools: (Pool | null)[];
}

export interface PoolMember {
  cpuMillis: number;
  draining: boolean;
  memoryBytes: number;
  serverId: string;
}

export interface Port {
  container: number;
  control?: boolean;
  name: string;
  protocol: string;
  routable?: boolean;
}

export interface PreferenceInputBody {
  deploymentEmail: boolean;
}

export interface Preferences {
  deploymentEmail: boolean;
  mailEnabled: boolean;
}

export interface Preview {
  after?: Target;
  afterAssignment?: Assignment;
  afterDefault?: DefaultPlacement;
  before?: Target;
  beforeAssignment?: Assignment;
  beforeDefault?: DefaultPlacement;
  blockers: string[];
  createdAt: string;
  environments: EnvironmentChange[];
  id: string;
  input: ChangeInput;
  orgId: string;
  projectId: string;
  reverts?: string;
  revision: string;
}

export interface Profile {
  createdAt: string;
  defaultPlacement: DefaultPlacement | null;
  id: string;
  name: string;
  orgId: string;
  projectId: string;
  revision: number;
  updatedAt: string;
}

export interface Project {
  color: string;
  createdAt: string;
  createdBy: string;
  description: string;
  icon: string;
  id: string;
  name: string;
  orgId: string;
  repositories: string[];
  slug: string;
  status: string;
  updatedAt: string;
}

export interface ProjectGrant {
  cloudId: string;
  orgId: string;
  projectId: string;
  revision: number;
  revoked: boolean;
  targetIds: string[];
}

export interface ProjectList {
  projects: Project[];
}

export interface ProjectView {
  graph?: Graph;
  projectId: string;
  sources: SourceState[];
}

export interface ProjectZone {
  id: string;
  mode: string;
  name: string;
  verified: boolean;
}

export interface ProjectZoneList {
  zones: ProjectZone[];
}

export interface Provider {
  available: boolean;
  buildLogs?: string;
  cloud: string;
  description: string;
  infrastructure?: string[];
  logNote?: string;
  model: string;
  name: string;
  note?: string;
  provisions?: Provisions[];
  regions?: string[];
  runs?: Runs[];
  scaling: boolean;
  title: string;
}

export interface ProviderList {
  providers: Provider[];
}

export interface ProviderStatus {
  capabilities: string[];
  name: string;
  region: string;
  version: string;
}

export interface Provisions {
  creates: string;
  kind: string;
  managedProfiles?: ManagedDatabaseProfile[];
  versions?: string[];
}

export interface PublicConfig {
  appId: string;
  authBasePath: string;
  publishableKey: string;
}

export interface RDSRecoveryNetwork {
  securityGroupId: string;
  subnetGroup: string;
  verifierGroupId: string;
  vpcId: string;
}

export interface RealtimeForgeGap {
  reason: string;
}

export interface RealtimeForgeResumed {
  count: number;
  from: string;
}

export interface RealtimeRealtimeSnapshot {
  cursor: string;
  partial: boolean;
  records: SnapshotRecord[];
}

export interface RealtimeRealtimeStatus {
  cursor: string;
  time: string;
}

export interface RealtimeReceive {
  channel: string;
  cursor: string;
  data: any;
  event: string;
}

export interface RealtimeSend {
  cursor?: string;
  deploymentIds?: string[];
  op: string;
  orgId: string;
  projectIds?: string[];
}

export interface Recipe {
  architecture: string;
  backupFormat: string;
  extensions?: string[];
  family?: string;
  fsGroup?: number;
  id: string;
  image: string;
  mountPath: string;
  productVersion: string;
  revision: string;
  volumeSizeMb?: number;
}

export interface RecoveryInputBody {
  planId: string;
  review?: string;
  service: string;
}

export interface Ref {
  component?: string;
  dependency?: string;
  field: string;
}

export interface RemoveRouteInputBody {
  revision: number;
}

export interface ReplaceInputBody {
  credential: string;
  settings?: Record<string, any>;
}

export interface Requirement {
  evidence: Evidence;
  git?: string;
  name: string;
  version?: string;
}

export interface ReservationRecovery {
  active: boolean;
  phase: string;
  planId: string;
  review: string;
  revision: number;
  serverId: string;
  service: string;
  started: boolean;
  storageRetained: boolean;
}

export interface Resolution {
  environment: string;
  projectId: string;
  service?: string;
  variables: ResolvedVariable[];
}

export interface ResolvedComponent {
  after?: string[];
  args?: string[];
  command?: string[];
  env?: EnvVar[];
  files?: File[];
  image: string;
  kubernetesRules?: KubernetesRule[];
  name: string;
  ports?: Port[];
  protection?: string;
  readiness?: HTTPProbe;
  requires?: string;
  resources: Resources;
  role: string;
  step: number;
  timeout?: number;
  volume?: Volume;
}

export interface ResolvedVariable {
  key: string;
  overrides: Variable[];
  variable: Variable;
}

export interface Resource {
  confidence: string;
  evidence: Evidence[];
  id: string;
  kind: string;
  needsConfirmation: boolean;
}

export interface ResourceConfig {
  catalog?: Recipe;
  cloudPlacement?: CloudPlacement;
  component?: ComponentConfig;
  composition?: CompositionConfig;
  kind: string;
  managed?: ManagedDatabaseConfig;
  provider: string;
  region?: string;
  routablePorts?: number[];
  target?: string;
  targetSettings?: Record<string, any>;
  version?: string;
}

export interface Resources {
  cpuMillis: number;
  memoryMb: number;
}

export interface Restore {
  backupId: string;
  createdAt: string;
  createdBy: string;
  durationMs: number;
  error?: string;
  expiresAt: string;
  externalId?: string;
  id: string;
  orgId: string;
  projectId: string;
  status: string;
  verifiedAt?: string;
}

export interface RestoreInputBody {
  requestId: string;
}

export interface RestoreList {
  restores: Restore[];
}

export interface RetainedPoolStorage {
  environmentId: string;
  planId: string;
  projectId: string;
  service: string;
}

export interface RetentionInput {
  keepLast: number;
  olderThanDays: number;
}

export interface RetentionPoint {
  backupId: string;
  bytes: number;
  createdAt: string;
  deletionSupported: boolean;
  deletionUnavailableReason?: string;
  eligible: boolean;
  reason: string;
}

export interface RetentionPolicy {
  anchorId: string;
  authorizedBy: string;
  createdAt: string;
  createdBy: string;
  enabled: boolean;
  id: string;
  lastError?: string;
  lastRetirementId?: string;
  nextAt: string;
  orgId: string;
  policy: RetentionInput;
  projectId: string;
  revision: number;
  updatedAt: string;
}

export interface RetentionPolicyHistory {
  events: RetentionPolicyRevision[];
  nextBefore?: string;
}

export interface RetentionPolicyInputBody {
  enabled: boolean;
  policy: {
  keepLast: number;
  olderThanDays: number;
};
  revision: number;
}

export interface RetentionPolicyRevision {
  authorizedBy: string;
  createdAt: string;
  enabled: boolean;
  policy: RetentionInput;
  revision: number;
}

export interface RetentionPolicyState {
  configured: boolean;
  policy?: RetentionPolicy;
}

export interface RetentionPreview {
  asOf: string;
  complete: boolean;
  eligibleBytes: number;
  eligibleCount: number;
  points: RetentionPoint[];
  verifiedBackupId?: string;
}

export interface Retirement {
  attempt: number;
  attemptLimit: number;
  backupId: string;
  completedAt?: string;
  createdAt: string;
  createdBy: string;
  error?: string;
  executionBy: string;
  id: string;
  orgId: string;
  policy: RetentionInput;
  projectId: string;
  retentionPolicyId?: string;
  retentionRevision?: number;
  status: string;
  witnessId: string;
}

export interface RetirementEvent {
  actor: string;
  attempt: number;
  attemptLimit: number;
  createdAt: string;
  cursor: string;
  error?: string;
  kind: string;
  previousActor?: string;
  previousError?: string;
  status: string;
}

export interface RetirementHistory {
  events: RetirementEvent[];
  nextBefore?: string;
}

export interface RetirementInputBody {
  policy: {
  keepLast: number;
  olderThanDays: number;
};
  requestId: string;
}

export interface RetirementPage {
  nextBefore?: string;
  retirements: Retirement[];
}

export interface RetirementRetryInputBody {
  requestId: string;
}

export interface Revision {
  createdAt: string;
  createdBy: string;
  digest: string;
  id: string;
  manifest?: Manifest;
  orgId: string;
  projectId: string;
  revision: number;
  yaml: string;
}

export interface RevisionList {
  revisions: Revision[];
}

export interface RouteBinding {
  address: string;
  bindingId: string;
  configDigest: string;
  connectionGeneration: number;
  connectionId: string;
  containerId: string;
  instanceId: string;
  releaseId: string;
  runtimeId: string;
  serverId: string;
  targetId: string;
}

export interface RouteInput {
  cloudId: string;
  environmentId: string;
  externalTtlSeconds?: number;
  hostname: string;
  port: number;
  service: string;
  zoneId: string;
}

export interface RouteInputBody {
  cloudId: string;
  environmentId: string;
  externalTtlSeconds?: number;
  hostname: string;
  port: number;
  service: string;
  zoneId: string;
}

export interface RouteOperation {
  attempt: number;
  createdAt: string;
  desiredRecord: DNSRecord;
  dnsAttempted: boolean;
  expiresAt: string;
  generation: number;
  handoffId?: string;
  id: string;
  kind: string;
  message: string;
  notBefore: string;
  previousReceipt?: DNSRecord;
  receipt?: DNSRecord;
  review: string;
  revision: number;
  route: CloudinfraRoute;
  source?: CloudinfraRoute;
  status: string;
}

export interface RouteTTLInputBody {
  externalTtlSeconds: number;
  revision: number;
}

export interface RouteVerification {
  backend: boolean;
  backendError?: string;
  certExpires: string;
  checkedAt: string;
  dns: boolean;
  dnsError?: string;
  tls: boolean;
  tlsError?: string;
}

export interface Runs {
  creates: string;
  execution: string;
  workload: string;
}

export interface RuntimeHealth {
  architecture: string;
  checkedAt: string;
  cpus: number;
  dockerId: string;
  effectiveCpuMillis: number;
  effectiveMemoryBytes: number;
  memoryBytes: number;
}

export interface SaveInputBody {
  baseRevision?: number;
  manifest?: {
  defaults?: Defaults;
  federation?: Federation;
  gateways?: Record<string, any>;
  held?: Held;
  ignore?: Ignore;
  links?: Record<string, any>;
  resources?: Record<string, any>;
  services?: Record<string, any>;
  version: number;
};
  yaml?: string;
}

export interface Scale {
  max: number;
  min: number;
}

export interface Server {
  accountId: string;
  cloudId: string;
  connectionId: string;
  createdAt: string;
  creationOperationId: string;
  draining: boolean;
  health: RuntimeHealth;
  id: string;
  name: string;
  orgId: string;
  ownership: string;
  provider: string;
  region: string;
  resourceId: string;
  revision: number;
  runtimeConnectionId: string;
  size: string;
  stateRevision: number;
  status: string;
  updatedAt: string;
}

export interface ServerImage {
  bootstrap: boolean;
  id: string;
  name: string;
  regions: string[];
}

export interface ServerInput {
  cloudRevision: number;
  confirmAdoption?: boolean;
  connectionId?: string;
  image?: string;
  kind: string;
  name?: string;
  region?: string;
  serverId?: string;
  serverRevision?: number;
  size?: string;
}

export interface ServerInputBody {
  cloudRevision: number;
  confirmAdoption?: boolean;
  connectionId?: string;
  image?: string;
  kind: string;
  name?: string;
  region?: string;
  serverId?: string;
  serverRevision?: number;
  size?: string;
}

export interface ServerList {
  servers: (Server | null)[];
}

export interface ServerOptions {
  images: ServerImage[];
  regions: ServerRegion[];
  sizes: ServerSize[];
}

export interface ServerRegion {
  id: string;
  name: string;
}

export interface ServerSize {
  cpus: number;
  diskGb: number;
  hourlyUsd: number;
  id: string;
  memoryMib: number;
  monthlyUsd: number;
  regions: string[];
}

export interface ServerUsage {
  draining: boolean;
  reservedCpuMillis: number;
  reservedMemoryBytes: number;
  retainedStorage: RetainedPoolStorage[];
  serverId: string;
  workloads: ServerWorkload[];
}

export interface ServerWorkload {
  active: boolean;
  bindingId?: string;
  cpuMillis: number;
  deploymentId?: string;
  environmentId: string;
  executionRevision: number;
  memoryBytes: number;
  phase: string;
  planId: string;
  poolId: string;
  projectId: string;
  service: string;
}

export interface Service {
  commands: Commands;
  confidence: string;
  context?: string;
  deployable: boolean;
  ecosystem: string;
  entrypoint: string;
  evidence: Evidence[];
  framework?: string;
  id: string;
  image?: string;
  kind: string;
  name: string;
  path: string;
  ports?: number[];
  requires?: Requirement[];
  runtime?: string;
  source?: string;
  unit?: string;
  unitName?: string;
}

export interface ServiceConfig {
  appliedInputs?: AppliedInputs;
  build?: string;
  buildTarget?: BuildTarget;
  catalog?: Recipe;
  cloudPlacement?: CloudPlacement;
  commitSha?: string;
  component?: ComponentConfig;
  context?: string;
  ecosystem: string;
  endpoint?: ManifestEndpoint;
  env?: EnvLink[];
  execution: string;
  framework?: string;
  gateway?: GatewayRoute[];
  healthPath?: string;
  image?: string;
  internal?: boolean;
  port?: number;
  privateEgress?: boolean;
  provider: string;
  public: boolean;
  region?: string;
  repo?: string;
  scale?: Scale;
  service: string;
  size?: Size;
  source: string;
  start?: string;
  target?: string;
  targetSettings?: Record<string, any>;
  workload: string;
}

export interface ServiceTraffic {
  bindingId: string;
  environmentId: string;
  latency: number;
  name: string;
  previousLatency: number;
  previousRequests: number;
  projectId: string;
  provider: string;
  requests: number;
  statistic?: string;
}

export interface SetDefaultPlacementInputBody {
  defaultPlacement: ({
  connectionId: string;
  provider: string;
  region: string;
}) | null;
  revision: number;
}

export interface Setting {
  key: string;
  label: string;
  required?: boolean;
}

export interface Size {
  cpu: number;
  memory: number;
}

export interface Snapshot {
  cursor: string;
  partial: boolean;
  records: SnapshotRecord[];
}

export interface SnapshotRecord {
  data: any;
  entityId: string;
  event: string;
  id: string;
  orgId: string;
  projectId?: string;
  time: string;
}

export interface Source {
  alias: string;
  createdAt: string;
  createdBy: string;
  id: string;
  orgId: string;
  projectId: string;
  ref: string;
  removedAt?: string;
  repository: string;
  root: string;
  updatedAt: string;
}

export interface SourceArtifact {
  alias: string;
  commitSha: string;
  key: string;
  repository: string;
}

export interface SourceList {
  sources: Source[];
}

export interface SourceRef {
  alias: string;
  analysisId: string;
  commitSha: string;
  repository: string;
  root?: string;
}

export interface SourceState {
  latest?: Analysis;
  shown?: Analysis;
  source: Source;
}

export interface SpendReport {
  accounts: AccountSpend[];
  month: string;
  sources: UsageSource[];
  totals: Amount[];
}

export interface StartAnalysisInputBody {
  ref?: string;
  sources?: string[];
}

export interface Step {
  actionId: string;
  finishedAt?: string;
  image: string;
  message: string;
  name: string;
  op: string;
  order: number;
  provider: string;
  startedAt?: string;
  status: string;
  subject: string;
  url: string;
  warnings?: string[];
}

export interface StorageVolumeList {
  volumes: StoredVolume[];
}

export interface StoredVolume {
  capacityBytes: number;
  connectionId: string;
  environmentId: string;
  id: string;
  instanceId: string;
  name: string;
  projectId: string;
  provider: string;
  role: string;
  status: string;
  storageClass?: string;
}

export interface Sync {
  after?: Target;
  afterDefault?: DefaultPlacement;
  attempt: number;
  before?: Target;
  beforeDefault?: DefaultPlacement;
  cloudId: string;
  createdAt: string;
  createdBy: string;
  default?: boolean;
  environments: EnvironmentChange[];
  error?: string;
  id: string;
  orgId: string;
  previewId: string;
  projectId: string;
  reverts?: string;
  status: string;
  targetId?: string;
  updatedAt: string;
}

export interface SyncRoute {
  cutoverId?: string;
  hostname: string;
  message?: string;
  notBefore: string;
  prepareId: string;
  recordType?: string;
  releaseId?: string;
  routeId: string;
  status: string;
  ttl?: number;
  value?: string;
}

export interface Target {
  cloudId: string;
  connectionId: string;
  createdAt: string;
  displayName: string;
  id: string;
  name: string;
  orgId: string;
  poolId?: string;
  projectId: string;
  provider: string;
  region: string;
  revision: number;
  runtime: string;
  tags: string[];
  updatedAt: string;
}

export interface TrafficReport {
  end: string;
  previousTotal: number;
  range: string;
  requests: Point[];
  services: ServiceTraffic[];
  sources: UsageSource[];
  start: string;
  step: number;
  total: number;
}

export interface UpdateInputBody {
  name: string;
  revision: number;
}

export interface UpdateProjectInputBody {
  color?: string;
  description?: string;
  icon?: string;
  name?: string;
}

export interface UpdateSourceInputBody {
  ref?: string;
  root?: string;
}

export interface UpdateTargetInputBody {
  displayName: string;
  revision: number;
  tags?: string[];
}

export interface UpdateVariableInputBody {
  secret?: boolean;
  value: string;
}

export interface UsageSource {
  cloud: string;
  connectionId: string;
  name: string;
  reason?: string;
  services?: string[];
  status: string;
}

export interface Use {
  enabled?: boolean;
  external?: string;
  resource?: string;
}

export interface Variable {
  createdAt: string;
  createdBy: string;
  environment: string;
  id: string;
  key: string;
  orgId: string;
  projectId?: string;
  scope: string;
  secret: boolean;
  service?: string;
  updatedAt: string;
  updatedBy: string;
  value: string;
}

export interface VariableList {
  variables: Variable[];
}

export interface Volume {
  fsGroup?: number;
  kubernetesSizeMb?: number;
  mountPath: string;
}

export interface WebhookEndpoint {
  createdAt: string;
  description: string;
  enabled: boolean;
  id: string;
  url: string;
}

export interface WebhookEndpointInputBody {
  description: string;
  url: string;
}

export interface WebhookEndpointList {
  items: WebhookEndpoint[];
}

export interface WebhookEndpointSecret {
  endpoint: WebhookEndpoint;
  secret: string;
}

export interface Workspace {
  ecosystem: string;
  evidence: Evidence[];
  members: string[];
  path: string;
  source?: string;
  tool: string;
}

export interface Zone {
  accountId?: string;
  connectionGeneration?: number;
  connectionId?: string;
  id: string;
  mode: string;
  name: string;
  orgId: string;
  projectIds: string[];
  proofExpiresAt: string;
  proofName?: string;
  proofValue?: string;
  provider?: string;
  providerZone: DNSZone;
  recordOwnership: boolean;
  revision: number;
  verifiedAt: string;
}

export interface ZoneInputBody {
  connectionId?: string;
  mode: string;
  name: string;
  projectIds: string[];
}

export interface ZoneList {
  zones: (Zone | null)[];
}

export interface ZoneUpdateBody {
  projectIds: string[];
  renewProof: boolean;
  revision: number;
}

export interface AuthConfig {
  bearerToken?: string;
  apiKey?: string;
  customHeaders?: Record<string, string>;
}

export interface ClientConfig {
  baseURL: string;
  auth?: AuthConfig;
  timeout?: number;
}

