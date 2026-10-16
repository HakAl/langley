import { describe, it, expect } from 'vitest'
import { parseLLMRequest, parseLLMResponse } from './llmFormat'

const sse = (events: Array<[string | null, unknown]>) =>
  events.map(([ev, data]) => `${ev ? `event: ${ev}\n` : ''}data: ${typeof data === 'string' ? data : JSON.stringify(data)}\n`).join('\n')

describe('parseLLMRequest', () => {
  it('parses Anthropic system prompt, message blocks, and tools', () => {
    const req = parseLLMRequest(JSON.stringify({
      model: 'claude-sonnet-4',
      system: [{ type: 'text', text: 'You are helpful.' }, { type: 'text', text: 'Be brief.' }],
      tools: [{ name: 'Read', input_schema: {} }, { name: 'Edit', input_schema: {} }],
      messages: [
        { role: 'user', content: 'hello' },
        { role: 'assistant', content: [
          { type: 'thinking', thinking: 'consider', signature: 'x' },
          { type: 'text', text: 'Reading file' },
          { type: 'tool_use', id: 'tu_1', name: 'Read', input: { path: 'a.go' } },
        ] },
        { role: 'user', content: [{ type: 'tool_result', tool_use_id: 'tu_1', content: [{ type: 'text', text: 'package a' }], is_error: true }] },
      ],
    }))
    expect(req).not.toBeNull()
    expect(req!.model).toBe('claude-sonnet-4')
    expect(req!.system).toBe('You are helpful.\nBe brief.')
    expect(req!.tools).toEqual(['Read', 'Edit'])
    expect(req!.messages).toHaveLength(3)
    expect(req!.messages[0]).toEqual({ role: 'user', blocks: [{ kind: 'text', text: 'hello' }] })
    expect(req!.messages[1].blocks).toEqual([
      { kind: 'thinking', text: 'consider' },
      { kind: 'text', text: 'Reading file' },
      { kind: 'tool_use', id: 'tu_1', name: 'Read', input: '{\n  "path": "a.go"\n}' },
    ])
    expect(req!.messages[2].blocks).toEqual([{ kind: 'tool_result', toolUseId: 'tu_1', content: 'package a', isError: true }])
  })

  it('parses OpenAI chat messages with tool_calls and tool role', () => {
    const req = parseLLMRequest(JSON.stringify({
      model: 'gpt-4o',
      tools: [{ type: 'function', function: { name: 'get_weather' } }],
      messages: [
        { role: 'system', content: 'sys' },
        { role: 'user', content: [{ type: 'text', text: 'weather?' }, { type: 'image_url', image_url: { url: 'x' } }] },
        { role: 'assistant', content: null, tool_calls: [{ id: 'call_1', type: 'function', function: { name: 'get_weather', arguments: '{"city":"Paris"}' } }] },
        { role: 'tool', tool_call_id: 'call_1', content: 'sunny' },
      ],
    }))
    expect(req!.system).toBeUndefined()
    expect(req!.tools).toEqual(['get_weather'])
    expect(req!.messages.map(m => m.role)).toEqual(['system', 'user', 'assistant', 'tool'])
    expect(req!.messages[1].blocks).toEqual([{ kind: 'text', text: 'weather?' }, { kind: 'other', label: 'image_url' }])
    expect(req!.messages[2].blocks).toEqual([{ kind: 'tool_use', id: 'call_1', name: 'get_weather', input: '{\n  "city": "Paris"\n}' }])
    expect(req!.messages[3].blocks).toEqual([{ kind: 'tool_result', toolUseId: 'call_1', content: 'sunny' }])
  })

  it('parses OpenAI Responses input items and instructions', () => {
    const req = parseLLMRequest(JSON.stringify({
      model: 'gpt-5',
      instructions: 'Be terse.',
      tools: [{ type: 'function', name: 'shell' }, { type: 'web_search' }],
      input: [
        { role: 'user', content: [{ type: 'input_text', text: 'list files' }] },
        { type: 'function_call', call_id: 'c1', name: 'shell', arguments: '{"cmd":"ls"}' },
        { type: 'function_call_output', call_id: 'c1', output: 'a.txt' },
      ],
    }))
    expect(req!.system).toBe('Be terse.')
    expect(req!.tools).toEqual(['shell', 'web_search'])
    expect(req!.messages).toEqual([
      { role: 'user', blocks: [{ kind: 'text', text: 'list files' }] },
      { role: 'assistant', blocks: [{ kind: 'tool_use', id: 'c1', name: 'shell', input: '{\n  "cmd": "ls"\n}' }] },
      { role: 'tool', blocks: [{ kind: 'tool_result', toolUseId: 'c1', content: 'a.txt' }] },
    ])
  })

  it('treats a string Responses input as a single user message', () => {
    const req = parseLLMRequest(JSON.stringify({ model: 'gpt-5', input: 'hi' }))
    expect(req!.messages).toEqual([{ role: 'user', blocks: [{ kind: 'text', text: 'hi' }] }])
    expect(req!.tools).toEqual([])
  })

  it('returns null for empty, invalid, truncated, or non-LLM bodies', () => {
    expect(parseLLMRequest(undefined)).toBeNull()
    expect(parseLLMRequest('')).toBeNull()
    expect(parseLLMRequest('{"messages": [')).toBeNull()
    expect(parseLLMRequest('[1,2]')).toBeNull()
    expect(parseLLMRequest('{"foo":"bar"}')).toBeNull()
  })
})

describe('parseLLMResponse (JSON)', () => {
  it('parses an Anthropic message', () => {
    const res = parseLLMResponse(JSON.stringify({
      type: 'message', role: 'assistant', model: 'claude-sonnet-4', stop_reason: 'tool_use',
      content: [{ type: 'text', text: 'ok' }, { type: 'tool_use', id: 't', name: 'Bash', input: { command: 'ls' } }],
    }))
    expect(res).toEqual({
      model: 'claude-sonnet-4', stopReason: 'tool_use', streamed: false,
      blocks: [{ kind: 'text', text: 'ok' }, { kind: 'tool_use', id: 't', name: 'Bash', input: '{\n  "command": "ls"\n}' }],
    })
  })

  it('parses an OpenAI chat completion with tool calls', () => {
    const res = parseLLMResponse(JSON.stringify({
      model: 'gpt-4o',
      choices: [{ finish_reason: 'tool_calls', message: { role: 'assistant', content: 'calling', tool_calls: [{ id: 'c', function: { name: 'f', arguments: '{}' } }] } }],
    }))
    expect(res!.stopReason).toBe('tool_calls')
    expect(res!.blocks).toEqual([{ kind: 'text', text: 'calling' }, { kind: 'tool_use', id: 'c', name: 'f', input: '{}' }])
  })

  it('parses an OpenAI Responses object', () => {
    const res = parseLLMResponse(JSON.stringify({
      object: 'response', model: 'gpt-5', status: 'completed',
      output: [
        { type: 'reasoning', summary: [{ type: 'summary_text', text: 'think' }] },
        { type: 'message', role: 'assistant', content: [{ type: 'output_text', text: 'done' }] },
      ],
    }))
    expect(res!.blocks).toEqual([{ kind: 'thinking', text: 'think' }, { kind: 'text', text: 'done' }])
    expect(res!.stopReason).toBe('completed')
  })

  it('returns null for error payloads and non-LLM JSON', () => {
    expect(parseLLMResponse('{"type":"error","error":{"message":"overloaded"}}')).toBeNull()
    expect(parseLLMResponse('{"status":"ok"}')).toBeNull()
    expect(parseLLMResponse('plain text')).toBeNull()
    expect(parseLLMResponse(null)).toBeNull()
  })
})

describe('parseLLMResponse (SSE)', () => {
  it('reconstructs an Anthropic stream with thinking, text, and tool_use', () => {
    const body = sse([
      ['message_start', { type: 'message_start', message: { model: 'claude-opus-4', content: [] } }],
      ['content_block_start', { type: 'content_block_start', index: 0, content_block: { type: 'thinking', thinking: '' } }],
      ['content_block_delta', { type: 'content_block_delta', index: 0, delta: { type: 'thinking_delta', thinking: 'Let me ' } }],
      ['content_block_delta', { type: 'content_block_delta', index: 0, delta: { type: 'thinking_delta', thinking: 'see.' } }],
      ['content_block_delta', { type: 'content_block_delta', index: 0, delta: { type: 'signature_delta', signature: 'sig' } }],
      ['content_block_start', { type: 'content_block_start', index: 1, content_block: { type: 'text', text: '' } }],
      ['content_block_delta', { type: 'content_block_delta', index: 1, delta: { type: 'text_delta', text: 'Hello' } }],
      ['content_block_delta', { type: 'content_block_delta', index: 1, delta: { type: 'text_delta', text: ' world' } }],
      ['content_block_start', { type: 'content_block_start', index: 2, content_block: { type: 'tool_use', id: 'tu', name: 'Read', input: {} } }],
      ['content_block_delta', { type: 'content_block_delta', index: 2, delta: { type: 'input_json_delta', partial_json: '{"pa' } }],
      ['content_block_delta', { type: 'content_block_delta', index: 2, delta: { type: 'input_json_delta', partial_json: 'th":"x"}' } }],
      ['message_delta', { type: 'message_delta', delta: { stop_reason: 'tool_use' }, usage: { output_tokens: 5 } }],
      ['message_stop', { type: 'message_stop' }],
    ])
    expect(parseLLMResponse(body)).toEqual({
      model: 'claude-opus-4',
      stopReason: 'tool_use',
      streamed: true,
      blocks: [
        { kind: 'thinking', text: 'Let me see.' },
        { kind: 'text', text: 'Hello world' },
        { kind: 'tool_use', id: 'tu', name: 'Read', input: '{\n  "path": "x"\n}' },
      ],
    })
  })

  it('keeps partial tool input when an Anthropic stream is truncated', () => {
    const body = sse([
      ['content_block_start', { type: 'content_block_start', index: 0, content_block: { type: 'tool_use', id: 'tu', name: 'Read', input: {} } }],
      ['content_block_delta', { type: 'content_block_delta', index: 0, delta: { type: 'input_json_delta', partial_json: '{"pa' } }],
    ])
    expect(parseLLMResponse(body)!.blocks).toEqual([{ kind: 'tool_use', id: 'tu', name: 'Read', input: '{"pa' }])
  })

  it('reconstructs an OpenAI chat stream with content and tool_call deltas', () => {
    const body = sse([
      [null, { model: 'gpt-4o', choices: [{ index: 0, delta: { role: 'assistant', content: 'Hi' } }] }],
      [null, { choices: [{ index: 0, delta: { content: ' there' } }] }],
      [null, { choices: [{ index: 0, delta: { tool_calls: [{ index: 0, id: 'call_1', function: { name: 'lookup', arguments: '{"q":' } }] } }] }],
      [null, { choices: [{ index: 0, delta: { tool_calls: [{ index: 0, function: { arguments: '"x"}' } }] } }] }],
      [null, { choices: [{ index: 0, delta: {}, finish_reason: 'tool_calls' }] }],
      [null, '[DONE]'],
    ])
    expect(parseLLMResponse(body)).toEqual({
      model: 'gpt-4o',
      stopReason: 'tool_calls',
      streamed: true,
      blocks: [
        { kind: 'text', text: 'Hi there' },
        { kind: 'tool_use', id: 'call_1', name: 'lookup', input: '{\n  "q": "x"\n}' },
      ],
    })
  })

  it('reconstructs an OpenAI Responses stream from output_text deltas', () => {
    const body = sse([
      ['response.created', { type: 'response.created', response: { model: 'gpt-5', output: [] } }],
      ['response.output_text.delta', { type: 'response.output_text.delta', item_id: 'msg_1', output_index: 0, delta: 'Hel' }],
      ['response.output_text.delta', { type: 'response.output_text.delta', item_id: 'msg_1', output_index: 0, delta: 'lo' }],
      ['response.output_item.added', { type: 'response.output_item.added', output_index: 1, item: { id: 'fc_1', type: 'function_call', call_id: 'c1', name: 'shell' } }],
      ['response.function_call_arguments.delta', { type: 'response.function_call_arguments.delta', item_id: 'fc_1', output_index: 1, delta: '{"cmd":"ls"}' }],
    ])
    expect(parseLLMResponse(body)).toEqual({
      model: 'gpt-5',
      stopReason: undefined,
      streamed: true,
      blocks: [
        { kind: 'text', text: 'Hello' },
        { kind: 'tool_use', id: 'c1', name: 'shell', input: '{\n  "cmd": "ls"\n}' },
      ],
    })
  })

  it('prefers the full response from response.completed when present', () => {
    const body = sse([
      ['response.output_text.delta', { type: 'response.output_text.delta', item_id: 'm', delta: 'partial' }],
      ['response.completed', { type: 'response.completed', response: { model: 'gpt-5', status: 'completed', output: [{ type: 'message', role: 'assistant', content: [{ type: 'output_text', text: 'final' }] }] } }],
    ])
    const res = parseLLMResponse(body)
    expect(res!.blocks).toEqual([{ kind: 'text', text: 'final' }])
    expect(res!.streamed).toBe(true)
  })

  it('handles CRLF line endings', () => {
    const body = 'event: content_block_delta\r\ndata: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}\r\n\r\n'
    expect(parseLLMResponse(body)!.blocks).toEqual([{ kind: 'text', text: 'ok' }])
  })

  it('returns null for SSE with no recognized LLM events', () => {
    expect(parseLLMResponse('event: ping\ndata: {"hello":1}\n\n')).toBeNull()
  })

  it('safely handles non-primitive discriminator objects without throwing', () => {
    const req = parseLLMRequest('{"messages":[{"role":"user","content":[{"type":{"toString":null}}]}]}')
    expect(req).not.toBeNull()
    expect(req!.messages).toEqual([
      { role: 'user', blocks: [{ kind: 'other', label: 'unknown' }] },
    ])

    const control = parseLLMRequest('{"messages":[{"role":"user","content":[{"type":"unknown_content"}]}]}')
    expect(control!.messages).toEqual([
      { role: 'user', blocks: [{ kind: 'other', label: 'unknown_content' }] },
    ])
  })

  it('preserves independent streamed and JSON Chat Completion choices without concatenating them', () => {
    const body = sse([
      [null, {
        model: 'gpt-4o',
        choices: [
          { index: 0, delta: { content: 'Answer A', tool_calls: [{ index: 0, id: 'call-a', function: { name: 'alpha', arguments: '{"a":1}' } }] } },
          { index: 1, delta: { content: 'Answer B', tool_calls: [{ index: 0, id: 'call-b', function: { name: 'beta', arguments: '{"b":2}' } }] } },
        ],
      }],
      [null, '[DONE]'],
    ])
    const res = parseLLMResponse(body)
    expect(res).toEqual({
      model: 'gpt-4o',
      stopReason: undefined,
      streamed: true,
      blocks: [
        { kind: 'other', label: 'choice 0' },
        { kind: 'text', text: 'Answer A' },
        { kind: 'tool_use', id: 'call-a', name: 'alpha', input: '{\n  "a": 1\n}' },
        { kind: 'other', label: 'choice 1' },
        { kind: 'text', text: 'Answer B' },
        { kind: 'tool_use', id: 'call-b', name: 'beta', input: '{\n  "b": 2\n}' },
      ],
    })

    const jsonRes = parseLLMResponse(JSON.stringify({
      model: 'gpt-4o',
      choices: [
        { index: 0, finish_reason: 'tool_calls', message: { role: 'assistant', content: 'Answer A', tool_calls: [{ id: 'call-a', function: { name: 'alpha', arguments: '{"a":1}' } }] } },
        { index: 1, finish_reason: 'tool_calls', message: { role: 'assistant', content: 'Answer B', tool_calls: [{ id: 'call-b', function: { name: 'beta', arguments: '{"b":2}' } }] } },
      ],
    }))
    expect(jsonRes!.blocks).toEqual(res!.blocks)
  })

  it('retains explicit stream errors from Anthropic error events and OpenAI response.failed events', () => {
    const anthropicFailed = parseLLMResponse(sse([
      ['message_start', { type: 'message_start', message: { model: 'claude-sonnet-4-6', role: 'assistant', content: [] } }],
      ['content_block_delta', { type: 'content_block_delta', index: 0, delta: { type: 'text_delta', text: 'Partial answer' } }],
      ['error', { type: 'error', error: { type: 'overloaded_error', message: 'Overloaded' } }],
    ]))
    expect(anthropicFailed).toEqual({
      model: 'claude-sonnet-4-6',
      stopReason: 'error',
      error: 'overloaded_error: Overloaded',
      streamed: true,
      blocks: [{ kind: 'text', text: 'Partial answer' }],
    })

    const responsesFailed = parseLLMResponse(sse([
      [null, { type: 'response.created', response: { model: 'gpt-4.1' } }],
      [null, { type: 'response.output_text.delta', item_id: 'message', delta: 'Partial answer' }],
      [null, { type: 'response.failed', response: { status: 'failed', error: { code: 'server_error', message: 'Synthetic failure' } } }],
    ]))
    expect(responsesFailed).toEqual({
      model: 'gpt-4.1',
      stopReason: 'failed',
      error: 'server_error: Synthetic failure',
      streamed: true,
      blocks: [{ kind: 'text', text: 'Partial answer' }],
    })
  })
})
