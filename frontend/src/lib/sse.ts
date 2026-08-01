export interface SSEEvent {
  event: string
  data: string
  id?: string
  retry?: number
}

export interface SSEParser {
  push(chunk: string): void
  finish(): void
}

interface EventState {
  event: string
  data: string[]
  id?: string
  retry?: number
}

function createEventState(): EventState {
  return { event: 'message', data: [] }
}

export function createSSEParser(onEvent: (event: SSEEvent) => void): SSEParser {
  let buffer = ''
  let state = createEventState()

  const dispatch = () => {
    if (state.data.length > 0) {
      onEvent({
        event: state.event,
        data: state.data.join('\n'),
        ...(state.id === undefined ? {} : { id: state.id }),
        ...(state.retry === undefined ? {} : { retry: state.retry }),
      })
    }
    state = createEventState()
  }

  const processLine = (line: string) => {
    if (line === '') {
      dispatch()
      return
    }
    if (line.startsWith(':')) return

    const separator = line.indexOf(':')
    const field = separator < 0 ? line : line.slice(0, separator)
    let value = separator < 0 ? '' : line.slice(separator + 1)
    if (value.startsWith(' ')) value = value.slice(1)

    if (field === 'data') state.data.push(value)
    if (field === 'event') state.event = value || 'message'
    if (field === 'id' && !value.includes('\0')) state.id = value
    if (field === 'retry' && /^\d+$/.test(value)) state.retry = Number(value)
  }

  const drainLines = () => {
    let newline = buffer.indexOf('\n')
    while (newline >= 0) {
      const line = buffer.slice(0, newline).replace(/\r$/, '')
      buffer = buffer.slice(newline + 1)
      processLine(line)
      newline = buffer.indexOf('\n')
    }
  }

  return {
    push(chunk) {
      buffer += chunk
      drainLines()
    },
    finish() {
      if (buffer) processLine(buffer.replace(/\r$/, ''))
      buffer = ''
      dispatch()
    },
  }
}
