import { useMemo, useSyncExternalStore } from 'react'

export const sessionGroupStorageKey = 'threave.session-groups.collapsed.v1'
const changeEvent = 'threave-session-groups-changed'
let fallbackSnapshot: string | null = null
let storageWriteFailed = false

export function useSessionGroupPreference() {
  const snapshot = useSyncExternalStore(subscribe, readSnapshot, () => null)
  const collapsedSessionIDs = useMemo(() => parseSnapshot(snapshot), [snapshot])

  function toggleSessionGroup(sessionID: string) {
    const next = parseSnapshot(readSnapshot())
    if (next.has(sessionID)) next.delete(sessionID)
    else next.add(sessionID)
    fallbackSnapshot = JSON.stringify([...next])
    try {
      window.localStorage.setItem(sessionGroupStorageKey, fallbackSnapshot)
      storageWriteFailed = false
    } catch {
      // Keep the lists usable when browser storage is unavailable.
      storageWriteFailed = true
    }
    window.dispatchEvent(new Event(changeEvent))
  }

  return { collapsedSessionIDs, toggleSessionGroup }
}

function readSnapshot(): string | null {
  if (storageWriteFailed) return fallbackSnapshot
  try {
    fallbackSnapshot = window.localStorage.getItem(sessionGroupStorageKey)
  } catch {
    // Retain this page's preference when browser storage is unavailable.
  }
  return fallbackSnapshot
}

function parseSnapshot(snapshot: string | null): Set<string> {
  try {
    const value: unknown = JSON.parse(snapshot ?? '[]')
    return new Set(Array.isArray(value) ? value.filter((id): id is string => typeof id === 'string') : [])
  } catch {
    return new Set()
  }
}

function subscribe(onChange: () => void) {
  const onStorage = (event: StorageEvent) => {
    if (event.key === sessionGroupStorageKey || event.key === null) {
      storageWriteFailed = false
      onChange()
    }
  }
  window.addEventListener(changeEvent, onChange)
  window.addEventListener('storage', onStorage)
  return () => {
    window.removeEventListener(changeEvent, onChange)
    window.removeEventListener('storage', onStorage)
  }
}
