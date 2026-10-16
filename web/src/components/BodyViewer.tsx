import { useState, useMemo } from 'react'
import { parseLLMRequest, parseLLMResponse } from '../llmFormat'
import type { ContentBlock, LLMMessage, LLMRequest, LLMResponse } from '../llmFormat'

interface BodyViewerProps {
  requestBody?: string | null
  responseBody?: string | null
  requestTruncated?: boolean
  responseTruncated?: boolean
  requestHeaders?: Record<string, string[]>
  responseHeaders?: Record<string, string[]>
}

type Tab = 'formatted' | 'request' | 'response' | 'headers'

const TAB_LABELS: Record<Tab, string> = {
  formatted: 'Formatted',
  request: 'Request',
  response: 'Response',
  headers: 'Headers',
}

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

function headersText(headers: Record<string, string[]>): string {
  return Object.entries(headers).map(([k, v]) => `${k}: ${v.join(', ')}`).join('\n')
}

function copyText(text: string) {
  navigator.clipboard?.writeText(text).catch(() => {})
}

function CopyButton({ text, label = 'Copy' }: { text: string; label?: string }) {
  return (
    <button className="secondary-btn body-copy-btn" onClick={() => copyText(text)} disabled={!text}>
      {label}
    </button>
  )
}

function Block({ block }: { block: ContentBlock }) {
  switch (block.kind) {
    case 'text':
      return <div className="llm-text">{block.text}</div>
    case 'thinking':
      return (
        <details className="llm-thinking">
          <summary>Thinking</summary>
          <div className="llm-text">{block.text || '(empty)'}</div>
        </details>
      )
    case 'tool_use':
      return (
        <div className="llm-tool-use">
          <div className="llm-tool-label">
            Tool call: <strong>{block.name}</strong>
            {block.id && <code className="llm-tool-id">{block.id}</code>}
          </div>
          {block.input && <pre>{block.input}</pre>}
        </div>
      )
    case 'tool_result':
      return (
        <div className={`llm-tool-result${block.isError ? ' llm-tool-error' : ''}`}>
          <div className="llm-tool-label">
            Tool result{block.isError && ' (error)'}
            {block.toolUseId && <code className="llm-tool-id">{block.toolUseId}</code>}
          </div>
          {block.content && <pre>{block.content}</pre>}
        </div>
      )
    case 'other':
      return <div className="llm-other">[{block.label}]</div>
  }
}

function Message({ message }: { message: LLMMessage }) {
  return (
    <div className={`llm-message llm-role-${message.role}`}>
      <div className="llm-role">{message.role}</div>
      {message.blocks.length === 0
        ? <div className="llm-other">(empty)</div>
        : message.blocks.map((b, i) => <Block key={i} block={b} />)}
    </div>
  )
}

function FormattedView({ request, response, truncated }: { request: LLMRequest | null; response: LLMResponse | null; truncated?: boolean }) {
  return (
    <div className="llm-formatted">
      {truncated && <div className="body-truncated">Captured body was truncated; conversation may be incomplete</div>}
      {request && (
        <section className="llm-section" aria-label="Request conversation">
          <h4 className="llm-section-title">Request{request.model && <span className="llm-meta">{request.model}</span>}</h4>
          {request.system && (
            <details className="llm-system" open={request.system.length <= 600}>
              <summary>System prompt <span className="llm-meta">{request.system.length.toLocaleString()} chars</span></summary>
              <div className="llm-text">{request.system}</div>
            </details>
          )}
          {request.tools.length > 0 && (
            <div className="llm-tools-summary">
              <strong>{request.tools.length} tool{request.tools.length === 1 ? '' : 's'}:</strong> {request.tools.join(', ')}
            </div>
          )}
          {request.messages.map((m, i) => <Message key={i} message={m} />)}
        </section>
      )}
      {response && (
        <section className="llm-section" aria-label="Assistant response">
          <h4 className="llm-section-title">
            Response
            {response.streamed && <span className="badge sse">SSE</span>}
            {response.stopReason && <span className="llm-meta">stop: {response.stopReason}</span>}
          </h4>
          <Message message={{ role: 'assistant', blocks: response.blocks }} />
        </section>
      )}
    </div>
  )
}

function RawView({ body, truncated, headers }: { body?: string | null; truncated?: boolean; headers?: Record<string, string[]> }) {
  const formatted = useMemo(() => (body ? formatBody(body) : ''), [body])
  if (!body) return <div className="body-empty">No body captured</div>
  const contentType = getContentType(headers)
  return (
    <>
      <div className="body-toolbar">
        <div>{contentType && <span className="badge body-badge">{contentType}</span>}</div>
        <CopyButton text={body} />
      </div>
      {truncated && <div className="body-truncated">Body was truncated (payload too large)</div>}
      <div className="body-content">
        <pre>{formatted}</pre>
      </div>
    </>
  )
}

function HeadersGroup({ title, headers }: { title: string; headers?: Record<string, string[]> }) {
  const entries = headers ? Object.entries(headers) : []
  return (
    <div className="headers-group">
      <div className="body-toolbar">
        <h4>{title}</h4>
        <CopyButton text={headers ? headersText(headers) : ''} />
      </div>
      <div className="body-content">
        {entries.length === 0
          ? <div className="body-empty">No headers captured</div>
          : entries.map(([k, v]) => (
            <div key={k} className="header-row"><span className="header-name">{k}:</span> {v.join(', ')}</div>
          ))}
      </div>
    </div>
  )
}

export function BodyViewer({
  requestBody,
  responseBody,
  requestTruncated,
  responseTruncated,
  requestHeaders,
  responseHeaders,
}: BodyViewerProps) {
  const parsedRequest = useMemo(() => parseLLMRequest(requestBody), [requestBody])
  const parsedResponse = useMemo(() => parseLLMResponse(responseBody), [responseBody])
  const hasFormatted = parsedRequest !== null || parsedResponse !== null
  const hasHeaders = !!requestHeaders || !!responseHeaders

  // null = follow the default, so the tab updates when full flow detail arrives after selection.
  const [chosenTab, setChosenTab] = useState<Tab | null>(null)

  if (!requestBody && !responseBody && !hasHeaders) return null

  const tabs: Tab[] = hasFormatted ? ['formatted', 'request', 'response', 'headers'] : ['request', 'response', 'headers']
  const defaultTab: Tab = hasFormatted ? 'formatted' : responseBody ? 'response' : requestBody ? 'request' : 'headers'
  const activeTab = chosenTab && tabs.includes(chosenTab) ? chosenTab : defaultTab

  return (
    <div className="body-viewer body-inspector">
      <div className="body-tabs" role="tablist" aria-label="Flow body">
        {tabs.map(t => (
          <button
            key={t}
            role="tab"
            id={`body-tab-${t}`}
            aria-selected={activeTab === t}
            aria-controls="body-panel"
            className={`body-tab${activeTab === t ? ' body-tab-active' : ''}`}
            onClick={() => setChosenTab(t)}
          >
            {TAB_LABELS[t]}
          </button>
        ))}
      </div>

      <div className="body-panel" id="body-panel" role="tabpanel" aria-labelledby={`body-tab-${activeTab}`} tabIndex={0}>
        {activeTab === 'formatted' && <FormattedView request={parsedRequest} response={parsedResponse} truncated={requestTruncated || responseTruncated} />}
        {activeTab === 'request' && <RawView body={requestBody} truncated={requestTruncated} headers={requestHeaders} />}
        {activeTab === 'response' && <RawView body={responseBody} truncated={responseTruncated} headers={responseHeaders} />}
        {activeTab === 'headers' && (
          <>
            <HeadersGroup title="Request Headers" headers={requestHeaders} />
            <HeadersGroup title="Response Headers" headers={responseHeaders} />
          </>
        )}
      </div>
    </div>
  )
}
