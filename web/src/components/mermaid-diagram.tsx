import { useEffect, useRef, useState, type ReactNode } from 'react'
import type { Mounted } from 'pele'

// Load the renderer only when a message actually contains a diagram.
export function MermaidDiagram({ source, children }: { source: string; children: ReactNode }) {
  const containerRef = useRef<HTMLDivElement>(null)
  const [status, setStatus] = useState<'loading' | 'ready' | 'error'>('loading')
  const [error, setError] = useState('')

  useEffect(() => {
    const container = containerRef.current
    if (!container) return
    let cancelled = false
    let diagram: Mounted | undefined
    setStatus('loading')
    setError('')

    void import('pele').then(({ mount }) => {
      if (cancelled) return
      diagram = mount(container, source, {
        // Chat content must not initiate image requests or navigate through SVG links.
        links: false,
        images: false,
        onRender: () => {
          if (!cancelled) setStatus('ready')
        },
        onError: (reason) => {
          if (cancelled) return
          setError(reason instanceof Error ? reason.message : 'Invalid diagram')
          setStatus('error')
        },
      })
    }).catch((reason: unknown) => {
      if (cancelled) return
      setError(reason instanceof Error ? reason.message : 'Diagram renderer unavailable')
      setStatus('error')
    })

    return () => {
      cancelled = true
      diagram?.destroy()
      container.replaceChildren()
    }
  }, [source])

  return (
    <div className="message-diagram my-2 w-[40rem] max-w-full overflow-hidden text-foreground">
      {status === 'loading' ? <p role="status" className="px-3 py-2 text-xs text-muted-foreground">Rendering diagram…</p> : null}
      <div ref={containerRef} role="region" aria-label="Mermaid diagram" hidden={status === 'error'} className="min-w-0 overflow-auto p-3" />
      {status === 'error' ? (
        <div className="px-3 pb-1">
          <p role="status" className="pt-2 text-xs text-muted-foreground">Unable to render Mermaid diagram: {error}</p>
          {children}
        </div>
      ) : (
        <details className="px-3 py-2">
          <summary className="cursor-pointer text-xs text-muted-foreground">Diagram source</summary>
          {children}
        </details>
      )}
    </div>
  )
}
