/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/

/**
 * 三种模态的交互结构不同，所以做成三个工作区而不是一个输入框加分支：
 *   chat  同步流式，结果是消息
 *   image 同步返回，结果是图片网格
 *   video 异步任务，结果是任务卡（进度 / 取消 / 过期）
 * 共用的是模型与分组选择、定价查表、金额 chip 与对账单骨架。
 */
export type PlaygroundMode = 'chat' | 'image' | 'video'

export interface ImageGenerationResultItem {
  url: string
  revisedPrompt?: string
}

export interface ImageGenerationResult {
  id: string
  createdAt: number
  items: ImageGenerationResultItem[]
  quota: number
  status: 'pending' | 'settled' | 'estimated'
  requestId: string
  error?: string
}

export type VideoTaskStatus = 'queued' | 'running' | 'succeeded' | 'failed'

export interface VideoTask {
  id: string
  prompt: string
  model: string
  createdAt: number
  status: VideoTaskStatus
  progress: number
  url: string
  error: string
  quota: number
  requestId: string
  durationSeconds: number
}
