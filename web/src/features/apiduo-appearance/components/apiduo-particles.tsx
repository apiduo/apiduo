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
import { useEffect, useRef, useState } from 'react'
import { createPortal } from 'react-dom'

import { useApiduoHoliday } from '../context'
import { particlesForHoliday } from '../holidays'

/**
 * The holiday ambience: one full-viewport canvas of particles plus, for the
 * spring festival, a row of swaying lanterns along the top edge.
 *
 * Nothing here renders when no holiday is active — the component unmounts
 * entirely, so a normal day pays zero cost for the feature. Nothing renders
 * under `prefers-reduced-motion` either: falling things are exactly the
 * category of animation that setting exists to suppress.
 *
 * The canvas sits at z-index 40, below dialogs (z-50) so a modal is never
 * snowed on, and takes no pointer events, so clicks pass through to the page.
 * Clicks are still observed on `document` and turned into a burst at that
 * spot — the sparkle is decoration, not an interaction.
 */
export function ApiduoParticles() {
  const holiday = useApiduoHoliday()
  const particles = holiday ? particlesForHoliday(holiday) : undefined
  const [reducedMotion, setReducedMotion] = useState(
    () =>
      typeof window !== 'undefined' &&
      window.matchMedia('(prefers-reduced-motion: reduce)').matches
  )

  useEffect(() => {
    const media = window.matchMedia('(prefers-reduced-motion: reduce)')
    const sync = () => setReducedMotion(media.matches)
    media.addEventListener('change', sync)
    return () => media.removeEventListener('change', sync)
  }, [])

  const canvasRef = useRef<HTMLCanvasElement | null>(null)
  const particlesRef = useRef(particles)
  particlesRef.current = particles

  useEffect(() => {
    if (!particles || reducedMotion) return

    const canvas = canvasRef.current
    const context = canvas?.getContext('2d')
    if (!canvas || !context) return

    let frame = 0
    const pool: Particle[] = []

    const fit = () => {
      canvas.width = window.innerWidth
      canvas.height = window.innerHeight
    }
    fit()
    window.addEventListener('resize', fit)

    const burst = (x: number, y: number) => {
      const cfg = particlesRef.current
      if (!cfg) return
      const [primary, secondary] = cfg.colors
      for (let i = 0; i < 60; i += 1) {
        const angle = Math.random() * Math.PI * 2
        const speed = 2 + Math.random() * 5
        pool.push({
          kind: 'confetti',
          x,
          y,
          vx: Math.cos(angle) * speed,
          vy: Math.sin(angle) * speed - 3,
          gravity: 0.14,
          life: 70 + Math.random() * 40,
          color: Math.random() < 0.5 ? primary : secondary,
          size: 2 + Math.random() * 3,
          spin: Math.random() * Math.PI * 2,
          spinRate: (Math.random() - 0.5) * 0.3,
        })
      }
    }

    const firework = (x: number, y: number) => {
      const cfg = particlesRef.current
      if (!cfg) return
      const [primary, secondary] = cfg.colors
      for (let i = 0; i < 70; i += 1) {
        const angle = (i / 70) * Math.PI * 2
        const speed = 1.5 + Math.random() * 3
        pool.push({
          kind: 'firework',
          x,
          y,
          vx: Math.cos(angle) * speed,
          vy: Math.sin(angle) * speed,
          gravity: 0.02,
          life: 60 + Math.random() * 30,
          color: Math.random() < 0.6 ? primary : secondary,
          size: 2.2,
          spin: 0,
        })
      }
    }

    const onClick = (event: MouseEvent) => {
      const cfg = particlesRef.current
      if (!cfg) return
      if (cfg.mode === 'confetti') burst(event.clientX, event.clientY)
      if (cfg.mode === 'firework') firework(event.clientX, event.clientY)
    }
    document.addEventListener('click', onClick, { passive: true })

    let raf = 0
    const tick = () => {
      const cfg = particlesRef.current
      if (!cfg) return
      const { mode, colors } = cfg
      const width = canvas.width
      const height = canvas.height
      const [primary, secondary] = colors
      frame += 1

      // Ambient spawns, one small chance per frame. Counts are bounded so the
      // pool can never grow without limit on a machine that never sleeps.
      if (mode === 'snow' && pool.length < 90) {
        pool.push({
          kind: 'snow',
          x: Math.random() * width,
          y: -8,
          vx: (Math.random() - 0.5) * 0.4,
          vy: 0.5 + Math.random() * 0.9,
          gravity: 0,
          life: Number.POSITIVE_INFINITY,
          color: 'rgba(238, 244, 255, 0.9)',
          size: 1 + Math.random() * 2.2,
          spin: 0,
        })
      }
      if (mode === 'pumpkin' && Math.random() < 0.02) {
        pool.push({
          kind: 'pumpkin',
          x: Math.random() * width,
          y: -30,
          vx: 0,
          vy: 0.6 + Math.random() * 0.8,
          gravity: 0,
          life: Number.POSITIVE_INFINITY,
          color: '',
          size: 26,
          spin: Math.random() * 0.5 - 0.25,
        })
      }
      if (mode === 'firework' && Math.random() < 0.012) {
        firework(Math.random() * width * 0.8 + width * 0.1, Math.random() * height * 0.45 + 20)
      }
      if (mode === 'confetti' && frame % 110 === 0) {
        burst(Math.random() * width, -6)
      }
      if (mode === 'moon' && !pool.some((p) => p.kind === 'moon')) {
        pool.push({
          kind: 'moon',
          x: width - 56,
          y: height * 0.24,
          vx: 0,
          vy: 0,
          gravity: 0,
          life: Number.POSITIVE_INFINITY,
          color: primary,
          size: 24,
          spin: 0,
        })
      }

      context.clearRect(0, 0, width, height)
      for (let i = pool.length - 1; i >= 0; i -= 1) {
        const p = pool[i]
        p.life -= 1
        p.x += p.vx
        p.y += p.vy
        p.vy += p.gravity

        if (p.kind === 'confetti') {
          p.spin += p.spinRate ?? 0
          context.globalAlpha = Math.max(p.life / 60, 0)
          context.fillStyle = p.color
          context.save()
          context.translate(p.x, p.y)
          context.rotate(p.spin)
          context.fillRect(-p.size, -p.size / 2, p.size * 2, p.size)
          context.restore()
          context.globalAlpha = 1
        } else if (p.kind === 'firework') {
          context.globalAlpha = Math.max(p.life / 60, 0)
          context.fillStyle = p.color
          context.beginPath()
          context.arc(p.x, p.y, p.size, 0, Math.PI * 2)
          context.fill()
          context.globalAlpha = 1
        } else if (p.kind === 'snow') {
          p.x += Math.sin(p.y * 0.02) * 0.3
          context.fillStyle = p.color
          context.beginPath()
          context.arc(p.x, p.y, p.size, 0, Math.PI * 2)
          context.fill()
          if (p.y > height + 8) pool.splice(i, 1)
        } else if (p.kind === 'pumpkin') {
          p.spin += 0.004
          context.save()
          context.translate(p.x, p.y)
          context.rotate(p.spin)
          context.font = `${p.size}px serif`
          context.textAlign = 'center'
          context.fillText('🎃', 0, 0)
          context.restore()
          if (p.y > height + 30) pool.splice(i, 1)
        } else if (p.kind === 'moon') {
          const glow = Math.min(p.spin + 0.008, 1)
          p.spin = glow
          context.globalAlpha = glow
          context.fillStyle = p.color
          context.shadowColor = secondary
          context.shadowBlur = 30
          context.beginPath()
          context.arc(p.x, p.y, p.size, 0, Math.PI * 2)
          context.fill()
          context.shadowBlur = 0
          for (let s = 0; s < 14; s += 1) {
            const sx = (s * 97.3) % width
            const sy = (s * 57.7) % (height * 0.6)
            const twinkle = 0.4 + 0.6 * Math.abs(Math.sin(frame * 0.03 + s))
            // Starlight shares the moon's gold: white stars vanish on a light
            // theme, gold ones read on both.
            context.globalAlpha = twinkle * 0.7 * glow
            context.fillStyle = secondary
            context.beginPath()
            context.arc(sx, sy, 1.3, 0, Math.PI * 2)
            context.fill()
          }
          context.globalAlpha = 1
        }

        if (p.life <= 0) pool.splice(i, 1)
      }

      raf = requestAnimationFrame(tick)
    }
    raf = requestAnimationFrame(tick)

    return () => {
      cancelAnimationFrame(raf)
      window.removeEventListener('resize', fit)
      document.removeEventListener('click', onClick)
    }
  }, [particles, reducedMotion])

  if (!particles || reducedMotion) return null

  return createPortal(
    <>
      <canvas ref={canvasRef} className='ap-particles' aria-hidden='true' />
      {particles.mode === 'confetti' && (
        <div className='ap-lanterns' aria-hidden='true'>
          <span style={{ animationDelay: '0s' }}>🏮</span>
          <span style={{ animationDelay: '0.7s' }}>🏮</span>
          <span style={{ animationDelay: '1.3s' }}>🏮</span>
        </div>
      )}
    </>,
    document.body
  )
}

type Particle = {
  kind: 'confetti' | 'firework' | 'snow' | 'pumpkin' | 'moon'
  x: number
  y: number
  vx: number
  vy: number
  gravity: number
  life: number
  color: string
  size: number
  spin: number
  spinRate?: number
}
