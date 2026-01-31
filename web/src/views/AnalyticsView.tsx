import type { Stats, CostPeriod } from '../types'

interface AnalyticsViewProps {
  stats: Stats | null
  dailyCosts: CostPeriod[]
}

export function AnalyticsView({ stats, dailyCosts }: AnalyticsViewProps) {
  return (
    <div className="analytics-view">
      {stats && (
        <div className="stats-grid">
          <div className="stat-card">
            <div className="stat-value">{stats.total_flows}</div>
            <div className="stat-label">Total Flows</div>
          </div>
          <div className="stat-card">
            <div className="stat-value">${stats.total_cost.toFixed(2)}</div>
            <div className="stat-label">Total Cost</div>
          </div>
          <div className="stat-card">
            <div className="stat-value">{(stats.total_tokens_in + stats.total_tokens_out).toLocaleString()}</div>
            <div className="stat-label">Total Tokens</div>
          </div>
          <div className="stat-card">
            <div className="stat-value">{stats.total_tasks}</div>
            <div className="stat-label">Tasks</div>
          </div>
          <div className="stat-card">
            <div className="stat-value">{stats.total_tool_calls}</div>
            <div className="stat-label">Tool Calls</div>
          </div>
          <div className="stat-card">
            <div className="stat-value">${stats.avg_cost_per_flow.toFixed(4)}</div>
            <div className="stat-label">Avg Cost/Flow</div>
          </div>
        </div>
      )}

      <div className="chart-section">
        <h3>Daily Cost</h3>
        {dailyCosts.length === 0 || dailyCosts.every(d => d.total_cost === 0) ? (
          <div className="chart-empty">
            <p>No cost data for this period</p>
            <p className="chart-empty-hint">Cost tracking requires LLM API traffic with token usage</p>
          </div>
        ) : (
          <div className="chart-bars">
            {dailyCosts.map((day, i) => {
              const maxCost = Math.max(...dailyCosts.map(d => d.total_cost), 0.01)
              const height = (day.total_cost / maxCost) * 100
              return (
                <div key={i} className="bar-container">
                  <span className="bar-value">${day.total_cost.toFixed(2)}</span>
                  <div className="bar" style={{ height: `${height}%` }}></div>
                  <span className="bar-label">{day.period.slice(5)}</span>
                </div>
              )
            })}
          </div>
        )}
      </div>
    </div>
  )
}
