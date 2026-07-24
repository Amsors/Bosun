import { authenticatedRequest, request } from './client'
import type {
  ClusterResourceSnapshot,
  ResizeAgentResourcesRequest,
  SessionResourceSnapshot,
} from './contracts'

export const monitorApi = {
  session: (token: string, sessionID: string) =>
    authenticatedRequest(token)<SessionResourceSnapshot>(
      `/sessions/${encodeURIComponent(sessionID)}/resources`,
    ),
  cluster: () => request<ClusterResourceSnapshot>('/admin/cluster'),
  resizeAgent: (sessionID: string, resources: ResizeAgentResourcesRequest) =>
    request<SessionResourceSnapshot>(`/admin/sessions/${encodeURIComponent(sessionID)}/resources`, {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(resources),
    }),
}
