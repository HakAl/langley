import { useEffect, useRef } from 'react'

interface HelpModalProps {
  onClose: () => void
}

export function HelpModal({ onClose }: HelpModalProps) {
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
    <div className="help-overlay" onClick={onClose}>
      <div className="help-modal" role="dialog" aria-modal="true" aria-labelledby="help-title" onClick={e => e.stopPropagation()}>
        <div className="help-header">
          <h2 id="help-title">Keyboard Shortcuts</h2>
          <button ref={closeRef} className="close-btn" onClick={onClose} aria-label="Close">×</button>
        </div>
        <table className="help-table">
          <tbody>
            <tr><td><kbd>j</kbd> / <kbd>k</kbd></td><td>Navigate down / up</td></tr>
            <tr><td><kbd>Enter</kbd></td><td>Select item</td></tr>
            <tr><td><kbd>/</kbd></td><td>Focus search (flows view)</td></tr>
            <tr><td><kbd>Escape</kbd></td><td>Close panel / blur input</td></tr>
            <tr><td><kbd>1</kbd>-<kbd>6</kbd></td><td>Switch views</td></tr>
            <tr><td><kbd>?</kbd></td><td>Toggle this help</td></tr>
          </tbody>
        </table>
      </div>
    </div>
  )
}
