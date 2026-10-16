import { TIME_RANGE_OPTIONS, timeRangeKey } from '../utils'

interface TimeRangeSelectProps {
  timeRange: number | null
  onTimeRangeChange: (days: number | null) => void
}

export function TimeRangeSelect({ timeRange, onTimeRangeChange }: TimeRangeSelectProps) {
  return (
    <select
      aria-label="Time range"
      value={timeRangeKey(timeRange)}
      onChange={(e) => {
        const opt = TIME_RANGE_OPTIONS.find(o => o.key === e.target.value)
        onTimeRangeChange(opt ? opt.days : null)
      }}
    >
      {TIME_RANGE_OPTIONS.map(o => (
        <option key={o.key} value={o.key}>{o.label}</option>
      ))}
    </select>
  )
}
