import { useEffect, useRef } from 'react'

interface ExportModalProps {
  format: string
  rowCount: number
  onConfirm: () => void
  onCancel: () => void
}

export function ExportModal({ format, rowCount, onConfirm, onCancel }: ExportModalProps) {
  const closeRef = useRef<HTMLButtonElement>(null)

  useEffect(() => {
    closeRef.current?.focus()
    const modal = closeRef.current?.closest('.help-modal') as HTMLElement | null
    if (!modal) return
    const trap = (e: KeyboardEvent) => {
      if (e.key !== 'Tab') return
      const focusable = modal.querySelectorAll<HTMLElement>('button, [href], [tabindex]:not([tabindex="-1"])')
      if (focusable.length === 0) return
      const first = focusable[0]
      const last = focusable[focusable.length - 1]
      if (e.shiftKey && document.activeElement === first) {
        e.preventDefault()
        last.focus()
      } else if (!e.shiftKey && document.activeElement === last) {
        e.preventDefault()
        first.focus()
      }
    }
    document.addEventListener('keydown', trap)
    return () => document.removeEventListener('keydown', trap)
  }, [])

  return (
    <div className="help-overlay" onClick={onCancel}>
      <div className="help-modal export-modal" role="dialog" aria-modal="true" aria-labelledby="export-title" onClick={e => e.stopPropagation()}>
        <div className="help-header">
          <h2 id="export-title">Export Flows</h2>
          <button ref={closeRef} className="close-btn" onClick={onCancel} aria-label="Close">×</button>
        </div>
        <div className="export-confirm-body">
          <p>
            Export <strong>{rowCount}</strong> flow{rowCount !== 1 ? 's' : ''} as <strong>{format.toUpperCase()}</strong>
          </p>
          {(format === 'csv' || format === 'json') && rowCount > 10000 && (
            <p className="warning">{format.toUpperCase()} exports are limited to 10,000 rows.</p>
          )}
        </div>
        <div className="export-confirm-actions">
          <button className="secondary-btn" onClick={onCancel}>Cancel</button>
          <button className="primary-btn" onClick={onConfirm}>Download</button>
        </div>
      </div>
    </div>
  )
}
