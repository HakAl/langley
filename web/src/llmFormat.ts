// Parses captured LLM request/response bodies (Anthropic Messages, OpenAI Chat
// Completions, OpenAI Responses; JSON or SSE) into a provider-neutral shape for display.

export type ContentBlock =
  | { kind: 'text'; text: string }
  | { kind: 'thinking'; text: string }
  | { kind: 'tool_use'; id?: string; name: string; input: string }
  | { kind: 'tool_result'; toolUseId?: string; content: string; isError?: boolean }
  | { kind: 'other'; label: string }

export interface LLMMessage {
  role: string
  blocks: ContentBlock[]
}

export interface LLMRequest {
  model?: string
  system?: string
  messages: LLMMessage[]
  tools: string[]
}

export interface LLMResponse {
  model?: string
  blocks: ContentBlock[]
  stopReason?: string
  streamed: boolean
}

type Obj = Record<string, unknown>

function isObj(v: unknown): v is Obj {
  return typeof v === 'object' && v !== null && !Array.isArray(v)
}

function str(v: unknown): string | undefined {
  return typeof v === 'string' ? v : undefined
}

function tryParseJSON(raw: string): unknown {
  try {
    return JSON.parse(raw)
  } catch {
    return undefined
  }
}

/** Pretty-prints a JSON value or JSON-encoded string; returns other strings unchanged. */
function prettyJSON(v: unknown): string {
  if (typeof v === 'string') {
    if (v.trim() === '') return ''
    const parsed = tryParseJSON(v)
    return parsed === undefined ? v : JSON.stringify(parsed, null, 2)
  }
  return v === undefined ? '' : JSON.stringify(v, null, 2)
}

/** Flattens text-ish content (string or array of text parts) to a single string. */
function contentText(v: unknown): string {
  if (typeof v === 'string') return v
  if (Array.isArray(v)) {
    return v
      .map(p => (typeof p === 'string' ? p : isObj(p) ? str(p.text) ?? (p.type ? `[${String(p.type)}]` : '') : ''))
      .filter(Boolean)
      .join('\n')
  }
  return v == null ? '' : prettyJSON(v)
}

/** Converts one Anthropic or OpenAI content part into a display block. */
function partToBlock(p: unknown): ContentBlock | null {
  if (typeof p === 'string') return p ? { kind: 'text', text: p } : null
  if (!isObj(p)) return null
  switch (p.type) {
    case 'text':
    case 'input_text':
    case 'output_text':
      return str(p.text) ? { kind: 'text', text: p.text as string } : null
    case 'thinking':
      return { kind: 'thinking', text: str(p.thinking) ?? '' }
    case 'redacted_thinking':
      return { kind: 'thinking', text: '[redacted]' }
    case 'tool_use':
    case 'server_tool_use':
      return { kind: 'tool_use', id: str(p.id), name: str(p.name) ?? 'tool', input: prettyJSON(p.input) }
    case 'tool_result':
      return { kind: 'tool_result', toolUseId: str(p.tool_use_id), content: contentText(p.content), isError: p.is_error === true }
    case 'refusal':
      return { kind: 'text', text: str(p.refusal) ?? '' }
    default:
      return { kind: 'other', label: String(p.type ?? 'unknown') }
  }
}

function contentToBlocks(content: unknown): ContentBlock[] {
  if (typeof content === 'string') return content ? [{ kind: 'text', text: content }] : []
  if (!Array.isArray(content)) return []
  return content.map(partToBlock).filter((b): b is ContentBlock => b !== null)
}

function openAIToolCalls(calls: unknown): ContentBlock[] {
  if (!Array.isArray(calls)) return []
  return calls.filter(isObj).map(c => {
    const fn = isObj(c.function) ? c.function : {}
    return { kind: 'tool_use' as const, id: str(c.id), name: str(fn.name) ?? 'function', input: prettyJSON(fn.arguments) }
  })
}

function parseChatMessage(m: Obj): LLMMessage {
  const role = str(m.role) ?? 'unknown'
  if (role === 'tool') {
    return { role, blocks: [{ kind: 'tool_result', toolUseId: str(m.tool_call_id), content: contentText(m.content) }] }
  }
  const blocks: ContentBlock[] = []
  if (str(m.reasoning_content)) blocks.push({ kind: 'thinking', text: m.reasoning_content as string })
  blocks.push(...contentToBlocks(m.content), ...openAIToolCalls(m.tool_calls))
  return { role, blocks }
}

/** Converts an OpenAI Responses API input/output item into a message. */
function parseResponsesItem(item: unknown): LLMMessage | null {
  if (!isObj(item)) return null
  switch (item.type) {
    case 'function_call':
      return { role: 'assistant', blocks: [{ kind: 'tool_use', id: str(item.call_id), name: str(item.name) ?? 'function', input: prettyJSON(item.arguments) }] }
    case 'function_call_output':
      return { role: 'tool', blocks: [{ kind: 'tool_result', toolUseId: str(item.call_id), content: contentText(item.output) }] }
    case 'reasoning': {
      const text = Array.isArray(item.summary) ? contentText(item.summary) : ''
      return { role: 'assistant', blocks: [{ kind: 'thinking', text }] }
    }
    case 'message':
    case undefined:
      if (!str(item.role)) return null
      return { role: item.role as string, blocks: contentToBlocks(item.content) }
    default:
      return { role: str(item.role) ?? 'assistant', blocks: [{ kind: 'other', label: String(item.type) }] }
  }
}

function toolNames(tools: unknown): string[] {
  if (!Array.isArray(tools)) return []
  return tools.filter(isObj).map(t => {
    const fn = isObj(t.function) ? t.function : undefined
    return str(t.name) ?? str(fn?.name) ?? str(t.type) ?? 'tool'
  })
}

/** Parses an LLM API request body. Returns null when the body is not a recognized LLM request. */
export function parseLLMRequest(body: string | null | undefined): LLMRequest | null {
  if (!body) return null
  const data = tryParseJSON(body)
  if (!isObj(data)) return null

  const model = str(data.model)
  const tools = toolNames(data.tools)

  if (Array.isArray(data.messages)) {
    const messages = data.messages.filter(isObj).map(parseChatMessage)
    const system = data.system != null ? contentText(data.system) : undefined
    return { model, system: system || undefined, messages, tools }
  }

  // OpenAI Responses API
  if (data.input !== undefined) {
    const messages = typeof data.input === 'string'
      ? [{ role: 'user', blocks: [{ kind: 'text' as const, text: data.input }] }]
      : Array.isArray(data.input)
        ? data.input.map(parseResponsesItem).filter((m): m is LLMMessage => m !== null)
        : null
    if (!messages) return null
    return { model, system: str(data.instructions) || undefined, messages, tools }
  }

  return null
}

function fromJSONResponse(data: Obj): LLMResponse | null {
  const model = str(data.model)

  // Anthropic Messages
  if (Array.isArray(data.content) && (data.type === 'message' || data.role === 'assistant')) {
    return { model, blocks: contentToBlocks(data.content), stopReason: str(data.stop_reason), streamed: false }
  }

  // OpenAI Chat Completions
  if (Array.isArray(data.choices)) {
    const choice = data.choices.find(isObj)
    if (!choice) return null
    const msg = isObj(choice.message) ? parseChatMessage(choice.message) : { role: 'assistant', blocks: [] }
    return { model, blocks: msg.blocks, stopReason: str(choice.finish_reason), streamed: false }
  }

  // OpenAI Responses
  if (Array.isArray(data.output)) {
    const blocks = data.output.map(parseResponsesItem).flatMap(m => (m ? m.blocks : []))
    return { model, blocks, stopReason: str(data.status), streamed: false }
  }

  return null
}

interface SSEEvent {
  event?: string
  data: string
}

function parseSSE(body: string): SSEEvent[] {
  const events: SSEEvent[] = []
  let event: string | undefined
  let data: string[] = []
  const flush = () => {
    if (data.length) events.push({ event, data: data.join('\n') })
    event = undefined
    data = []
  }
  for (const line of body.split(/\r?\n/)) {
    if (line === '') flush()
    else if (line.startsWith('event:')) event = line.slice(6).trim()
    else if (line.startsWith('data:')) data.push(line.slice(5).replace(/^ /, ''))
  }
  flush()
  return events
}

/** Accumulates streamed blocks by index, concatenating deltas. */
class StreamAccumulator {
  private blocks = new Map<string, ContentBlock>()
  private order: string[] = []

  get(key: string, init: () => ContentBlock): ContentBlock {
    let b = this.blocks.get(key)
    if (!b) {
      b = init()
      this.blocks.set(key, b)
      this.order.push(key)
    }
    return b
  }

  append(key: string, kind: 'text' | 'thinking', delta: string) {
    const b = this.get(key, () => ({ kind, text: '' }))
    if (b.kind === 'text' || b.kind === 'thinking') b.text += delta
  }

  appendInput(key: string, delta: string) {
    const b = this.get(key, () => ({ kind: 'tool_use', name: 'tool', input: '' }))
    if (b.kind === 'tool_use') b.input += delta
  }

  result(): ContentBlock[] {
    return this.order.map(k => {
      const b = this.blocks.get(k) as ContentBlock
      return b.kind === 'tool_use' ? { ...b, input: prettyJSON(b.input) } : b
    }).filter(b => !(b.kind === 'text' && b.text === ''))
  }
}

function fromSSEResponse(body: string): LLMResponse | null {
  const events = parseSSE(body)
  if (events.length === 0) return null

  const acc = new StreamAccumulator()
  let model: string | undefined
  let stopReason: string | undefined
  let completed: LLMResponse | null = null
  let recognized = false

  for (const ev of events) {
    if (ev.data === '[DONE]') continue
    const d = tryParseJSON(ev.data)
    if (!isObj(d)) continue
    const type = str(d.type) ?? ev.event

    // Anthropic Messages streaming
    if (type === 'message_start' && isObj(d.message)) {
      recognized = true
      model = str(d.message.model) ?? model
      continue
    }
    if (type === 'content_block_start' && isObj(d.content_block)) {
      recognized = true
      const key = `a${d.index}`
      const cb = d.content_block
      if (cb.type === 'tool_use' || cb.type === 'server_tool_use') {
        acc.get(key, () => ({ kind: 'tool_use', id: str(cb.id), name: str(cb.name) ?? 'tool', input: '' }))
      } else if (cb.type === 'thinking' || cb.type === 'redacted_thinking') {
        acc.append(key, 'thinking', cb.type === 'redacted_thinking' ? '[redacted]' : str(cb.thinking) ?? '')
      } else if (cb.type === 'text') {
        acc.append(key, 'text', str(cb.text) ?? '')
      } else {
        const block = partToBlock(cb)
        if (block) acc.get(key, () => block)
      }
      continue
    }
    if (type === 'content_block_delta' && isObj(d.delta)) {
      recognized = true
      const key = `a${d.index}`
      const delta = d.delta
      if (delta.type === 'text_delta') acc.append(key, 'text', str(delta.text) ?? '')
      else if (delta.type === 'thinking_delta') acc.append(key, 'thinking', str(delta.thinking) ?? '')
      else if (delta.type === 'input_json_delta') acc.appendInput(key, str(delta.partial_json) ?? '')
      continue
    }
    if (type === 'message_delta' && isObj(d.delta)) {
      recognized = true
      stopReason = str(d.delta.stop_reason) ?? stopReason
      continue
    }

    // OpenAI Responses streaming
    if (type?.startsWith('response.')) {
      recognized = true
      const out = str(d.item_id) ?? String(d.output_index ?? 0)
      if (type === 'response.output_text.delta') acc.append(`r-text-${out}`, 'text', str(d.delta) ?? '')
      else if (type === 'response.reasoning_summary_text.delta' || type === 'response.reasoning_text.delta') acc.append(`r-think-${out}`, 'thinking', str(d.delta) ?? '')
      else if (type === 'response.output_item.added' && isObj(d.item) && d.item.type === 'function_call') {
        const item = d.item
        acc.get(`r-fn-${str(item.id) ?? String(d.output_index ?? 0)}`, () => ({ kind: 'tool_use', id: str(item.call_id), name: str(item.name) ?? 'function', input: '' }))
      } else if (type === 'response.function_call_arguments.delta') acc.appendInput(`r-fn-${out}`, str(d.delta) ?? '')
      else if ((type === 'response.completed' || type === 'response.incomplete') && isObj(d.response)) {
        completed = fromJSONResponse(d.response)
        model = str(d.response.model) ?? model
      } else if (type === 'response.created' && isObj(d.response)) {
        model = str(d.response.model) ?? model
      }
      continue
    }

    // OpenAI Chat Completions streaming
    if (Array.isArray(d.choices)) {
      recognized = true
      model = str(d.model) ?? model
      for (const choice of d.choices.filter(isObj)) {
        if (str(choice.finish_reason)) stopReason = choice.finish_reason as string
        const delta = isObj(choice.delta) ? choice.delta : undefined
        if (!delta) continue
        if (str(delta.reasoning_content)) acc.append('c-think', 'thinking', delta.reasoning_content as string)
        if (str(delta.content)) acc.append('c-text', 'text', delta.content as string)
        if (Array.isArray(delta.tool_calls)) {
          for (const tc of delta.tool_calls.filter(isObj)) {
            const key = `c-tool-${tc.index ?? 0}`
            const fn = isObj(tc.function) ? tc.function : {}
            const b = acc.get(key, () => ({ kind: 'tool_use', id: str(tc.id), name: str(fn.name) ?? 'function', input: '' }))
            if (b.kind === 'tool_use') {
              if (str(tc.id)) b.id = tc.id as string
              if (str(fn.name)) b.name = fn.name as string
            }
            acc.appendInput(key, str(fn.arguments) ?? '')
          }
        }
      }
    }
  }

  if (completed && completed.blocks.length > 0) {
    return { ...completed, model: completed.model ?? model, streamed: true }
  }
  if (!recognized) return null
  return { model, blocks: acc.result(), stopReason: stopReason ?? completed?.stopReason, streamed: true }
}

/** Parses an LLM API response body (JSON or SSE). Returns null when not a recognized LLM response. */
export function parseLLMResponse(body: string | null | undefined): LLMResponse | null {
  if (!body) return null
  const trimmed = body.trimStart()
  if (trimmed.startsWith('{')) {
    const data = tryParseJSON(trimmed)
    if (isObj(data)) return fromJSONResponse(data)
  }
  return fromSSEResponse(body)
}
