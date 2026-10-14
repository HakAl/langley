import { useState, useEffect } from 'react'
import type { ApiResult, Settings } from '../types'

interface SettingsViewProps {
  settings: Settings | null
  onSave: (settings: Partial<Settings>) => Promise<ApiResult<Settings>>
}

export function SettingsView({ settings, onSave }: SettingsViewProps) {
  const [idleGapInput, setIdleGapInput] = useState(5)
  const [saving, setSaving] = useState(false)
  const [saved, setSaved] = useState(false)
  const [saveError, setSaveError] = useState<string | null>(null)

  useEffect(() => {
    if (settings) setIdleGapInput(settings.idle_gap_minutes)
  }, [settings])

  const isValid = idleGapInput >= 1 && idleGapInput <= 60
  const isDirty = idleGapInput !== settings?.idle_gap_minutes

  const handleSave = async () => {
    setSaving(true)
    setSaveError(null)
    const { data, error } = await onSave({ idle_gap_minutes: idleGapInput })
    setSaving(false)
    if (data) {
      setSaved(true)
      setTimeout(() => setSaved(false), 3000)
    }
    if (error) setSaveError(error)
  }

  return (
    <div className="settings-view">
      <h2>Settings</h2>

      <div className="settings-section">
        <h3>Task Grouping</h3>
        <p className="settings-description">
          Langley groups API calls into tasks based on timing. When there's a gap of inactivity,
          a new task is started.
        </p>

        <div className="setting-row">
          <label htmlFor="idle-gap">Idle Gap (minutes)</label>
          <div className="setting-input-group">
            <input
              id="idle-gap"
              type="number"
              min={1}
              max={60}
              value={idleGapInput}
              onChange={(e) => setIdleGapInput(parseInt(e.target.value) || 1)}
            />
            <span className="setting-hint">1-60 minutes</span>
            {!isValid && <span className="setting-error" role="alert">Value must be between 1 and 60</span>}
          </div>
          <p className="setting-help">
            Minutes of inactivity before starting a new task. Lower values create more granular tasks.
          </p>
        </div>
      </div>

      <div className="settings-actions">
        <button
          className="primary-btn"
          onClick={handleSave}
          disabled={saving || !isValid || !isDirty}
        >
          {saving ? 'Saving...' : 'Save Settings'}
        </button>
        {saved && <span className="settings-saved" role="status">Settings saved</span>}
        {saveError && <span className="setting-error" role="alert">{saveError}</span>}
        {isDirty && (
          <button
            className="secondary-btn"
            onClick={() => setIdleGapInput(settings?.idle_gap_minutes ?? 5)}
          >
            Reset
          </button>
        )}
      </div>

      <div className="settings-info">
        <h3>Note</h3>
        <p>
          Changes to task grouping settings apply to new API calls only. Existing tasks
          are not affected.
        </p>
      </div>
    </div>
  )
}
