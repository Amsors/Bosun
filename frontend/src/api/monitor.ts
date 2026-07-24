import { authenticatedRequest, request } from './client'
import type { ClusterResourceSnapshot, SessionResourceSnapshot } from './contracts'

export const monitorApi = {
  session: (token: string, sessionID: string) =>
    authenticatedRequest(token)<SessionResourceSnapshot>(
      `/sessions/${encodeURIComponent(sessionID)}/resources`,
    ),
  cluster: () => request<ClusterResourceSnapshot>('/admin/cluster'),
}
