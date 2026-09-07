// sessionLabels — derived labels for the session list (ADR-0041 §3). The API
// has no session titles, so the list derives a human label from the wire
// fields; nothing here talks to the engine.
import type { Session } from "../generated/types.gen";
import type { SessionState } from "../state/store";

/** Primary label: mode if set, else "selection" for anchors, else "chat". */
export function sessionLabel(s: Session): string {
  if (s.modeType) return s.modeType;
  if (s.anchorBlockId) return "selection";
  return "chat";
}

/** Secondary line: relative age of last activity. */
export function sessionSubLabel(s: Session, now = Date.now()): string {
  const ts = s.updatedAt ?? s.createdAt;
  if (!ts) return "";
  const age = Math.max(0, now - ts);
  const minutes = Math.floor(age / 60_000);
  if (minutes < 1) return "just now";
  if (minutes < 60) return `${minutes}m ago`;
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return `${hours}h ago`;
  const days = Math.floor(hours / 24);
  return days === 1 ? "yesterday" : `${days}d ago`;
}

/** Most-recently-active first. */
export function sortSessions(list: Session[]): Session[] {
  return [...list].sort((a, b) => (b.updatedAt ?? 0) - (a.updatedAt ?? 0));
}

/** Whether a session currently has a turn streaming (concurrency, ADR-0026 §4). */
export function sessionWorking(
  states: Record<string, SessionState>,
  id: string,
): boolean {
  return states[id]?.turn.active ?? false;
}
