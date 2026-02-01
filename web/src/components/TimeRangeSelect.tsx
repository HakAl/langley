interface TimeRangeSelectProps {
  timeRange: number | null
  onTimeRangeChange: (days: number | null) => void
}

export function TimeRangeSelect({ timeRange, onTimeRangeChange }: TimeRangeSelectProps) {
  return (
    <select
      aria-label="Time range"
      value={timeRange == null ? 'all' : String(timeRange)}
      onChange={(e) => onTimeRangeChange(e.target.value === 'all' ? null : Number(e.target.value))}
    >
      <option value="1">Last 24 hours</option>
      <option value="7">Last 7 days</option>
      <option value="30">Last 30 days</option>
      <option value="90">Last 90 days</option>
      <option value="all">All time</option>
    </select>
  )
}
