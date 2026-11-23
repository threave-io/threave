import type { MessageAttachment, SkillReference, SubmitAgentOptions, SubmitMessageResponse } from '@/lib/api'

export type PendingSubmission = {
  id: string
  sessionID: string
  content: string
  options?: SubmitAgentOptions
  attachments: MessageAttachment[]
  queue: boolean
  steerRunID?: string
  skills: SkillReference[]
  createdAt: string
}
export type SubmissionStatus = {
  state: 'not_received' | 'unknown' | 'accepted' | 'rejected'
  target_run_id?: string
  run_ended?: boolean
  response?: SubmitMessageResponse
}
export const pendingSubmissionsChanged = 'gorchestra:pending-submissions-changed'
const prefix = 'gorchestra.pending-submission.v1.'

async function database() {
  return new Promise<IDBDatabase>((resolve, reject) => {
    const request = indexedDB.open('gorchestra-pending-submissions', 1)
    request.onupgradeneeded = () => request.result.createObjectStore('submissions', { keyPath: 'id' })
    request.onerror = () => reject(request.error)
    request.onblocked = () => reject(new Error('Pending-message storage is blocked by another tab.'))
    request.onsuccess = () => resolve(request.result)
  })
}

async function transact<T>(mode: IDBTransactionMode, run: (store: IDBObjectStore) => IDBRequest<T>): Promise<T> {
  const db = await database()
  try {
    return await new Promise<T>((resolve, reject) => {
      const transaction = db.transaction('submissions', mode)
      const request = run(transaction.objectStore('submissions'))
      transaction.oncomplete = () => resolve(request.result)
      transaction.onerror = () => reject(transaction.error ?? request.error)
      transaction.onabort = () => reject(transaction.error ?? new Error('Pending-message storage failed.'))
    })
  } finally { db.close() }
}

export async function readPendingSubmissions(sessionID: string): Promise<PendingSubmission[]> {
  let values: PendingSubmission[]
  if (typeof indexedDB !== 'undefined') {
    values = await transact('readonly', (store) => store.getAll())
  } else {
    values = []
    for (let index = 0; index < window.localStorage.length; index++) {
      const key = window.localStorage.key(index)!
      if (key.startsWith(prefix)) values.push(JSON.parse(window.localStorage.getItem(key)!))
    }
  }
  return values.filter((value) => value.sessionID === sessionID).sort((a, b) => a.createdAt.localeCompare(b.createdAt))
}

export async function savePendingSubmission(value: PendingSubmission) {
  try {
    if (typeof indexedDB !== 'undefined') await transact('readwrite', (store) => store.put(value))
    else window.localStorage.setItem(prefix + value.id, JSON.stringify(value))
  } catch {
    throw new Error('Unable to preserve this pending message on your device. Nothing was sent. Free local storage or copy the draft before leaving.')
  }
  window.dispatchEvent(new Event(pendingSubmissionsChanged))
}

export async function removePendingSubmission(id: string) {
  if (typeof indexedDB !== 'undefined') await transact('readwrite', (store) => store.delete(id))
  else window.localStorage.removeItem(prefix + id)
  window.dispatchEvent(new Event(pendingSubmissionsChanged))
}
