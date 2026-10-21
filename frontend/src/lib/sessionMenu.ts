import type { Session } from "./tabs";
import type { SessionRecordingMenuState } from "./sessionLogging";

type MenuSession = Pick<Session, "id" | "processInstanceId"> & {
  loggingControl?: SessionRecordingMenuState;
};

export function sessionContextMenu(session: MenuSession, tiled: boolean): { name: string; target: string } {
  const target = `${session.id}:${session.processInstanceId}`;
  const control = session.loggingControl;
  let loggingAction = "idle";
  if (control?.target === target) {
    if (control.canStop) loggingAction = "stop";
    else if (control.canStart) loggingAction = "start";
  }
  return { name: `session-${tiled ? "tile" : "tab"}-${loggingAction}`, target };
}

// Pin destructive and layout actions to the right-clicked process too. A menu
// left open through a reconnect must not act on the replacement session.
export function sessionForContextMenu<S extends MenuSession>(sessions: readonly S[], menuData: unknown): S | null {
  if (typeof menuData !== "string") return null;
  return sessions.find((session) => `${session.id}:${session.processInstanceId}` === menuData) ?? null;
}
