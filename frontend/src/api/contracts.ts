export type UUID = string
export type RFC3339Timestamp = string

export interface ApiEnvelope<T> {
  code: number
  message: string
  data: T
}

export interface PaginatedData<T> {
  items: T[]
  total: number
}

export interface User {
  id: UUID
  email: string
  createdAt: RFC3339Timestamp
}

export interface AuthData {
  accessToken: string
  tokenType: string
  accessExpiresAt: RFC3339Timestamp
  user: User
}

export interface MeData {
  user: User
  environmentPhase: EnvironmentPhase
}

export const apiErrorCodes = {
  invalidArgument: 10001,
  idempotencyConflict: 10002,
  invalidCredentials: 20001,
  tokenExpired: 20002,
  rateLimited: 20003,
  sessionNotFound: 30001,
  invalidTransition: 30002,
  capacityUnavailable: 30003,
  sessionNotRunning: 30004,
  environmentFailed: 30005,
  environmentNotReady: 30006,
  internalError: 50001,
} as const

export type EnvironmentPhase = 'Pending' | 'Ready' | 'Failed'
export type DesiredState = 'Running' | 'Hibernated'
export type SessionTier = 'small' | 'medium'
export type SessionPriority = 'low' | 'normal' | 'high'
export type Runtime = 'claude-code'
export type ProviderMode = 'platform' | 'byok'
export type StoragePolicy = 'local' | 'archive'
export type SessionPhase =
  | 'Pending'
  | 'Provisioning'
  | 'Running'
  | 'Idle'
  | 'Hibernating'
  | 'Hibernated'
  | 'Archiving'
  | 'Archived'
  | 'Restoring'
  | 'Deleting'
  | 'Failed'

export interface Condition {
  type: string
  status: 'True' | 'False' | 'Unknown'
  observedGeneration?: number
  lastTransitionTime: RFC3339Timestamp
  reason: string
  message: string
}

export interface ProviderSelection {
  mode: ProviderMode
  credentialID?: UUID
}

export interface CreateSessionRequest {
  name: string
  priority: SessionPriority
  tier: SessionTier
  runtime: Runtime
  provider: ProviderSelection
  storagePolicy: StoragePolicy
}

export interface Session {
  id: UUID
  name: string
  priority: SessionPriority
  desiredState: DesiredState
  tier: SessionTier
  runtime: Runtime
  provider: ProviderSelection
  storagePolicy: StoragePolicy
  phase: SessionPhase
  phaseReason?: string
  lastActiveAt?: RFC3339Timestamp
  conditions: Condition[]
  createdAt: RFC3339Timestamp
}

export interface TerminalFrame {
  t: 'stdin' | 'stdout' | 'resize'
  d: string
}

export const terminalSubprotocol = 'bosun-terminal-v1'
export const terminalReconnectDelaysMs = [1000, 2000, 4000, 8000, 15000] as const
