// 三种模态的上游 mock：chat(SSE/JSON)、images、video 任务状态机
const http = require('http')

const PNG_1X1 =
  'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=='

const videoTasks = new Map()
const hits = []

function json(res, body, code = 200) {
  res.writeHead(code, { 'Content-Type': 'application/json' })
  res.end(JSON.stringify(body))
}

function readBody(req) {
  return new Promise((resolve) => {
    let raw = ''
    req.on('data', (c) => {
      raw += c
    })
    req.on('end', () => {
      try {
        resolve(JSON.parse(raw || '{}'))
      } catch {
        resolve({})
      }
    })
  })
}

http
  .createServer(async (req, res) => {
    const url = req.url.split('?')[0]
    hits.push(req.method + ' ' + url)

    if (req.method === 'GET' && url === '/__stats') {
      return json(res, { hits })
    }
    if (req.method === 'GET' && url === '/__reset') {
      hits.length = 0
      videoTasks.clear()
      return json(res, { ok: true })
    }

    // ---- 视频任务查询（sora 协议用 /v1/videos/:id） ----
    if (req.method === 'GET' && (url.startsWith('/v1/video/generations/') || url.startsWith('/v1/videos/'))) {
      const id = url.split('/').pop()
      const task = videoTasks.get(id) || { polls: 0 }
      task.polls += 1
      videoTasks.set(id, task)
      // sora 插件只认 OpenAI 官方枚举：queued / in_progress / completed / failed
      // 后台轮询约 30s 一轮，这里把状态机压到两轮内到终态
      if (task.polls < 1) {
        return json(res, { id, status: 'queued', progress: 10 })
      }
      if (task.polls < 2) {
        return json(res, { id, status: 'in_progress', progress: 60 })
      }
      return json(res, {
        id,
        status: 'completed',
        progress: 100,
        video_url: 'https://example.invalid/demo.mp4',
      })
    }

    if (req.method !== 'POST') {
      return json(res, { error: { message: 'not found' } }, 404)
    }

    const body = await readBody(req)

    // ---- 生视频（sora 插件打到 /v1/videos） ----
    if (url.includes('/video/') || url === '/v1/videos') {
      const id = 'task_' + Math.random().toString(36).slice(2, 10)
      videoTasks.set(id, { polls: 0 })
      return json(res, { id, status: 'queued', progress: 0 })
    }

    // ---- 生图 ----
    if (url.includes('/images/generations')) {
      const n = Number(body.n) || 1
      return json(res, {
        created: Math.floor(Date.now() / 1000),
        data: Array.from({ length: n }, () => ({ b64_json: PNG_1X1 })),
      })
    }

    // ---- 对话 ----
    if (url.includes('/chat/completions')) {
      const model = body.model || 'gpt-4o'
      const parts = ['本地 mock：', '这是一条', '可复现的', '测试回复。']
      const usage = {
        prompt_tokens: body.__promptTokens || 1200,
        completion_tokens: body.__completionTokens || 380,
        total_tokens: 1580,
        prompt_tokens_details: { cached_tokens: body.__cacheTokens ?? 800 },
      }

      if (!body.stream) {
        return json(res, {
          id: 'chatcmpl-mock',
          object: 'chat.completion',
          created: Math.floor(Date.now() / 1000),
          model,
          choices: [
            {
              index: 0,
              message: { role: 'assistant', content: parts.join('') },
              finish_reason: 'stop',
            },
          ],
          usage,
        })
      }

      res.writeHead(200, {
        'Content-Type': 'text/event-stream',
        'Cache-Control': 'no-cache',
        Connection: 'keep-alive',
      })
      let i = 0
      const timer = setInterval(() => {
        if (i < parts.length) {
          res.write(
            'data: ' +
              JSON.stringify({
                id: 'chatcmpl-mock',
                object: 'chat.completion.chunk',
                created: Math.floor(Date.now() / 1000),
                model,
                choices: [{ index: 0, delta: { content: parts[i] }, finish_reason: null }],
              }) +
              '\n\n'
          )
          i += 1
          return
        }
        res.write(
          'data: ' +
            JSON.stringify({
              id: 'chatcmpl-mock',
              object: 'chat.completion.chunk',
              created: Math.floor(Date.now() / 1000),
              model,
              choices: [],
              usage,
            }) +
            '\n\n'
        )
        res.write('data: [DONE]\n\n')
        clearInterval(timer)
        res.end()
      }, 60)
      return
    }

    return json(res, { error: { message: 'unhandled path: ' + url } }, 404)
  })
  .listen(3911, '127.0.0.1', () => console.log('mock upstream :3911'))
