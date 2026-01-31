interface ExportModalProps {
  format: string
  rowCount: number
  onConfirm: () => void
  onCancel: () => void
}

export function ExportModal({ format, rowCount, onConfirm, onCancel }: ExportModalProps) {
  return (
    <div className="help-overlay" onClick={onCancel}>
      <div className="help-modal export-modal" onClick={e => e.stopPropagation()}>
        <div className="help-header">
          <h2>Export Flows</h2>
          <button className="close-btn" onClick={onCancel}>×</button>
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
