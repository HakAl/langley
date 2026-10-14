import { useState, useMemo } from 'react'

interface BodyViewerProps {
  requestBody?: string | null
  responseBody?: string | null
  requestTruncated?: boolean
  responseTruncated?: boolean
  requestHeaders?: Record<string, string[]>
  responseHeaders?: Record<string, string[]>
}

type Tab = 'request' | 'response'

const LINE_LIMIT = 20

function getContentType(headers?: Record<string, string[]>): string | null {
  if (!headers) return null
  const key = Object.keys(headers).find(k => k.toLowerCase() === 'content-type')
  if (!key) return null
  const val = headers[key]?.[0]
  return val?.split(';')[0]?.trim() ?? null
}

function formatBody(raw: string): string {
  try {
    return JSON.stringify(JSON.parse(raw), null, 2)
  } catch {
    return raw
  }
}

export function BodyViewer({
  requestBody,
  responseBody,
  requestTruncated,
  responseTruncated,
  requestHeaders,
  responseHeaders,
}: BodyViewerProps) {
  const hasRequest = !!requestBody
  const hasResponse = !!responseBody
  if (!hasRequest && !hasResponse) return null

  const defaultTab: Tab = hasResponse ? 'response' : 'request'
  const [activeTab, setActiveTab] = useState<Tab>(defaultTab)
  const [expanded, setExpanded] = useState(false)

  function switchTab(tab: Tab) {
    setActiveTab(tab)
    setExpanded(false)
  }

  const body = activeTab === 'request' ? requestBody : responseBody
  const truncated = activeTab === 'request' ? requestTruncated : responseTruncated
  const headers = activeTab === 'request' ? requestHeaders : responseHeaders
  const contentType = getContentType(headers)

  const formatted = useMemo(() => (body ? formatBody(body) : ''), [body])
  const lines = formatted.split('\n')
  const needsCollapse = lines.length > LINE_LIMIT
  const isCollapsed = needsCollapse && !expanded

  function handleCopy() {
    if (body) navigator.clipboard.writeText(body)
  }

  return (
    <div className="body-viewer">
      <div className="body-tabs" role="tablist">
        <button
          role="tab"
          aria-selected={activeTab === 'request'}
          className={`body-tab${activeTab === 'request' ? ' body-tab-active' : ''}`}
          onClick={() => switchTab('request')}
        >
          Request
        </button>
        <button
          role="tab"
          aria-selected={activeTab === 'response'}
          className={`body-tab${activeTab === 'response' ? ' body-tab-active' : ''}`}
          onClick={() => switchTab('response')}
        >
          Response
        </button>
      </div>

      {body ? (
        <>
          <div className="body-toolbar">
            <div>{contentType && <span className="badge body-badge">{contentType}</span>}</div>
            <button className="secondary-btn body-copy-btn" onClick={handleCopy}>
              Copy
            </button>
          </div>
          {truncated && (
            <div className="body-truncated">Body was truncated (payload too large)</div>
          )}
          <div className={`body-content${isCollapsed ? ' body-collapsed' : ''}`}>
            <pre>{isCollapsed ? lines.slice(0, LINE_LIMIT).join('\n') : formatted}</pre>
          </div>
          {needsCollapse && (
            <button
              className="secondary-btn body-expand-btn"
              onClick={() => setExpanded(e => !e)}
            >
              {expanded ? 'Collapse' : `Show all (${lines.length} lines)`}
            </button>
          )}
        </>
      ) : (
        <div className="body-empty">No body captured</div>
      )}
    </div>
  )
}
