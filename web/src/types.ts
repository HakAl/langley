export interface Flow {
  id: string
  host: string
  method: string
  path: string
  url?: string
  status_code?: number
  status_text?: string
  is_sse: boolean
  timestamp: string
  duration_ms?: number
  task_id?: string
  task_source?: string
  model?: string
  input_tokens?: number
  output_tokens?: number
  cache_creation_tokens?: number
  cache_read_tokens?: number
  total_cost?: number
  cost_source?: string
  provider?: string
  flow_integrity?: string
  events_dropped_count?: number
  request_body?: string
  response_body?: string
  request_headers?: Record<string, string[]>
  response_headers?: Record<string, string[]>
}

export interface WSMessage {
  type: string
  timestamp: string
  data: Flow
}

export interface Stats {
  status: string
  total_flows: number
  all_time_flows: number
  total_cost: number
  total_tokens_in: number
  total_tokens_out: number
  total_tasks: number
  total_tool_calls: number
  avg_cost_per_flow: number
}

export interface TaskSummary {
  task_id: string
  flow_count: number
  total_tokens_in: number
  total_tokens_out: number
  total_cost: number
  first_seen: string
  last_seen: string
  duration_ms: number
}

export interface ToolStats {
  tool_name: string
  invocation_count: number
  success_count: number
  failure_count: number
  success_rate: number
  total_cost: number
  avg_duration_ms: number
}

export interface Anomaly {
  type: string
  flow_id: string
  task_id?: string
  timestamp: string
  severity: string
  description: string
  value: number
  threshold: number
}

export interface CostPeriod {
  period: string
  flow_count: number
  total_cost: number
  total_tokens_in: number
  total_tokens_out: number
}

export interface Settings {
  idle_gap_minutes: number
}

export type View = 'flows' | 'analytics' | 'tasks' | 'tools' | 'anomalies' | 'settings'
