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
  error?: string
  streamed: boolean
}

type Obj = Record<string, unknown>

function isObj(v: unknown): v is Obj {
  return typeof v === 'object' && v !== null && !Array.isArray(v)
}

function str(v: unknown): string | undefined {
  return typeof v === 'string' ? v : undefined
}

/** Safely converts an untrusted JSON discriminator/index to a display label without invoking custom object coercion. */
function safeLabel(v: unknown, fallback = 'unknown'): string {
  if (typeof v === 'string' && v.length > 0) return v
  if (typeof v === 'number' || typeof v === 'boolean') return String(v)
  return fallback
}

function formatErrorValue(err: unknown): string | undefined {
  if (typeof err === 'string' && err) return err
  if (!isObj(err)) return undefined
  const code = str(err.type) ?? str(err.code)
  const msg = str(err.message)
  if (code && msg) return `${code}: ${msg}`
  return msg ?? code
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
      .map(p => (typeof p === 'string' ? p : isObj(p) ? str(p.text) ?? (p.type !== undefined ? `[${safeLabel(p.type)}]` : '') : ''))
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
      return { kind: 'other', label: safeLabel(p.type) }
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
      return { role: str(item.role) ?? 'assistant', blocks: [{ kind: 'other', label: safeLabel(item.type) }] }
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
  try {
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
  } catch {
    return null
  }
}

function fromJSONResponse(data: Obj): LLMResponse | null {
  const model = str(data.model)

  // Anthropic Messages
  if (Array.isArray(data.content) && (data.type === 'message' || data.role === 'assistant')) {
    return { model, blocks: contentToBlocks(data.content), stopReason: str(data.stop_reason), streamed: false }
  }

  // OpenAI Chat Completions
  if (Array.isArray(data.choices)) {
    const choices = data.choices.filter(isObj)
    if (choices.length === 0) return null
    const blocks: ContentBlock[] = []
    let stopReason: string | undefined
    for (let i = 0; i < choices.length; i++) {
      const choice = choices[i]
      if (str(choice.finish_reason)) stopReason = stopReason ?? (choice.finish_reason as string)
      const msg = isObj(choice.message) ? parseChatMessage(choice.message) : { role: 'assistant', blocks: [] }
      if (choices.length > 1 && msg.blocks.length > 0) {
        blocks.push({ kind: 'other', label: `choice ${safeLabel(choice.index, String(i))}` })
      }
      blocks.push(...msg.blocks)
    }
    return { model, blocks, stopReason, streamed: false }
  }

  // OpenAI Responses
  if (Array.isArray(data.output) || (data.status === 'failed' && data.error !== undefined)) {
    const blocks = Array.isArray(data.output)
      ? data.output.map(parseResponsesItem).flatMap(m => (m ? m.blocks : []))
      : []
    const error = formatErrorValue(data.error)
    return { model, blocks, stopReason: str(data.status), ...(error ? { error } : {}), streamed: false }
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
  const choiceAccs = new Map<string, StreamAccumulator>()
  let model: string | undefined
  let stopReason: string | undefined
  let streamError: string | undefined
  let completed: LLMResponse | null = null
  let recognized = false

  const getChoiceAcc = (cIdx: string): StreamAccumulator => {
    let cAcc = choiceAccs.get(cIdx)
    if (!cAcc) {
      cAcc = new StreamAccumulator()
      choiceAccs.set(cIdx, cAcc)
    }
    return cAcc
  }

  for (const ev of events) {
    if (ev.data === '[DONE]') continue
    const d = tryParseJSON(ev.data)
    if (!isObj(d)) continue
    const type = str(d.type) ?? ev.event

    // Anthropic or generic SSE stream error event
    if (type === 'error' || (d.error !== undefined && !type?.startsWith('response.'))) {
      const errMsg = formatErrorValue(d.error) ?? formatErrorValue(d)
      if (errMsg) {
        recognized = true
        streamError = errMsg
        stopReason = stopReason ?? 'error'
      }
      if (type === 'error') continue
    }

    // Anthropic Messages streaming
    if (type === 'message_start' && isObj(d.message)) {
      recognized = true
      model = str(d.message.model) ?? model
      continue
    }
    if (type === 'content_block_start' && isObj(d.content_block)) {
      recognized = true
      const key = `a${safeLabel(d.index, '0')}`
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
      const key = `a${safeLabel(d.index, '0')}`
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
      const out = str(d.item_id) ?? safeLabel(d.output_index, '0')
      if (type === 'response.output_text.delta') acc.append(`r-text-${out}`, 'text', str(d.delta) ?? '')
      else if (type === 'response.reasoning_summary_text.delta' || type === 'response.reasoning_text.delta') acc.append(`r-think-${out}`, 'thinking', str(d.delta) ?? '')
      else if (type === 'response.output_item.added' && isObj(d.item) && d.item.type === 'function_call') {
        const item = d.item
        acc.get(`r-fn-${str(item.id) ?? safeLabel(d.output_index, '0')}`, () => ({ kind: 'tool_use', id: str(item.call_id), name: str(item.name) ?? 'function', input: '' }))
      } else if (type === 'response.function_call_arguments.delta') acc.appendInput(`r-fn-${out}`, str(d.delta) ?? '')
      else if ((type === 'response.completed' || type === 'response.incomplete') && isObj(d.response)) {
        completed = fromJSONResponse(d.response)
        model = str(d.response.model) ?? model
      } else if (type === 'response.failed') {
        if (isObj(d.response)) {
          completed = fromJSONResponse(d.response)
          model = str(d.response.model) ?? model
          stopReason = str(d.response.status) ?? 'failed'
          streamError = formatErrorValue(d.response.error) ?? formatErrorValue(d.error) ?? 'Stream failed'
        } else {
          stopReason = 'failed'
          streamError = formatErrorValue(d.error) ?? 'Stream failed'
        }
      } else if (type === 'response.created' && isObj(d.response)) {
        model = str(d.response.model) ?? model
      }
      continue
    }

    // OpenAI Chat Completions streaming
    if (Array.isArray(d.choices)) {
      recognized = true
      model = str(d.model) ?? model
      const choices = d.choices.filter(isObj)
      for (let i = 0; i < choices.length; i++) {
        const choice = choices[i]
        const cIdx = safeLabel(choice.index, String(i))
        const cAcc = getChoiceAcc(cIdx)
        if (str(choice.finish_reason)) stopReason = stopReason ?? (choice.finish_reason as string)
        const delta = isObj(choice.delta) ? choice.delta : undefined
        if (!delta) continue
        if (str(delta.reasoning_content)) cAcc.append('think', 'thinking', delta.reasoning_content as string)
        if (str(delta.content)) cAcc.append('text', 'text', delta.content as string)
        if (Array.isArray(delta.tool_calls)) {
          for (const tc of delta.tool_calls.filter(isObj)) {
            const key = `tool-${safeLabel(tc.index, '0')}`
            const fn = isObj(tc.function) ? tc.function : {}
            const b = cAcc.get(key, () => ({ kind: 'tool_use', id: str(tc.id), name: str(fn.name) ?? 'function', input: '' }))
            if (b.kind === 'tool_use') {
              if (str(tc.id)) b.id = tc.id as string
              if (str(fn.name)) b.name = fn.name as string
            }
            cAcc.appendInput(key, str(fn.arguments) ?? '')
          }
        }
      }
    }
  }

  if (completed && completed.blocks.length > 0) {
    const err = streamError ?? completed.error
    return {
      ...completed,
      model: completed.model ?? model,
      stopReason: completed.stopReason ?? stopReason,
      ...(err ? { error: err } : {}),
      streamed: true,
    }
  }
  if (!recognized) return null

  const blocks = acc.result()
  if (choiceAccs.size > 0) {
    for (const [cIdx, cAcc] of choiceAccs.entries()) {
      const cBlocks = cAcc.result()
      if (choiceAccs.size > 1 && cBlocks.length > 0) {
        blocks.push({ kind: 'other', label: `choice ${cIdx}` })
      }
      blocks.push(...cBlocks)
    }
  }

  const err = streamError ?? completed?.error
  return {
    model,
    blocks,
    stopReason: stopReason ?? completed?.stopReason,
    ...(err ? { error: err } : {}),
    streamed: true,
  }
}

/** Parses an LLM API response body (JSON or SSE). Returns null when not a recognized LLM response. */
export function parseLLMResponse(body: string | null | undefined): LLMResponse | null {
  try {
    if (!body) return null
    const trimmed = body.trimStart()
    if (trimmed.startsWith('{')) {
      const data = tryParseJSON(trimmed)
      if (isObj(data)) return fromJSONResponse(data)
    }
    return fromSSEResponse(body)
  } catch {
    return null
  }
}
