import { loadPreferences, preferenceKeys, type PreferenceStorage } from "./storage.ts";

export type BackupPreferenceMap = Record<string, string>;
export type BackupPreferenceStorage = Pick<Storage, "getItem" | "setItem" | "removeItem">;

const transferableKeys = [
  preferenceKeys.sidebarWidth,
  preferenceKeys.sidebarVisible,
  preferenceKeys.tilingMode,
  preferenceKeys.rightClickToPaste,
  preferenceKeys.copyOnSelection,
  preferenceKeys.checkUpdatesOnStartup,
  preferenceKeys.terminalScrollbackLines,
  preferenceKeys.collapsedFolders,
  preferenceKeys.terminalPasteWarningsDisabled,
  preferenceKeys.themeName,
  preferenceKeys.terminalColors,
  preferenceKeys.uiSize,
  preferenceKeys.terminalFontSize,
  preferenceKeys.interfaceScale,
  preferenceKeys.terminalFontName,
] as const;

const transferableKeySet = new Set<string>(transferableKeys);
const utf8Encoder = new TextEncoder();

function trimFolderWhitespace(value: string): string {
  return value.replace(/^\p{White_Space}+|\p{White_Space}+$/gu, "");
}

function isNormalizedFolderPath(value: string): boolean {
  if (!value || utf8Encoder.encode(value).byteLength > 255) {
    return false;
  }
  const segments = value.split("/");
  return segments.every((segment) => {
    const trimmed = trimFolderWhitespace(segment);
    return trimmed.length > 0
      && trimmed !== "."
      && trimmed !== ".."
      && !/\p{Cc}/u.test(trimmed)
      && trimmed === segment;
  });
}

function canonicalPreferences(preferences: ReturnType<typeof loadPreferences>): BackupPreferenceMap {
  return {
    [preferenceKeys.sidebarWidth]: String(preferences.sidebarWidth),
    [preferenceKeys.sidebarVisible]: String(preferences.sidebarVisible),
    [preferenceKeys.tilingMode]: String(preferences.tilingMode),
    [preferenceKeys.rightClickToPaste]: String(preferences.rightClickToPaste),
    [preferenceKeys.copyOnSelection]: String(preferences.copyOnSelection),
    [preferenceKeys.checkUpdatesOnStartup]: String(preferences.checkUpdatesOnStartup),
    [preferenceKeys.terminalScrollbackLines]: String(preferences.terminalScrollbackLines),
    [preferenceKeys.collapsedFolders]: JSON.stringify(Object.keys(preferences.collapsedFolders).sort()),
    [preferenceKeys.terminalPasteWarningsDisabled]: String(preferences.terminalPasteWarningsDisabled),
    [preferenceKeys.themeName]: preferences.themeName,
    [preferenceKeys.terminalColors]: preferences.terminalColors,
    [preferenceKeys.uiSize]: preferences.uiSize,
    [preferenceKeys.terminalFontSize]: String(preferences.terminalFontSize),
    [preferenceKeys.interfaceScale]: preferences.interfaceScale.toFixed(1),
    [preferenceKeys.terminalFontName]: preferences.terminalFontName,
  };
}

/**
 * Capture the settings that affect the current UI. Read storage directly first
 * so restricted storage does not silently turn into a default-only backup.
 */
export function captureBackupPreferences(storage: PreferenceStorage): BackupPreferenceMap {
  const values = new Map<string, string | null>();
  for (const key of transferableKeys) {
    values.set(key, storage.getItem(key));
  }

  const readableStorage: PreferenceStorage = {
    getItem(key) {
      return values.get(key) ?? null;
    },
    setItem() {
      throw new Error("Backup preference capture is read-only.");
    },
  };
  return validateBackupPreferences(canonicalPreferences(loadPreferences(readableStorage)));
}

function validInteger(value: string, minimum: number, maximum: number): boolean {
  if (!/^(?:0|[1-9]\d*)$/.test(value)) {
    return false;
  }
  const number = Number(value);
  return Number.isSafeInteger(number) && number >= minimum && number <= maximum;
}

function isValidPreferenceValue(key: string, value: string): boolean {
  switch (key) {
    case preferenceKeys.sidebarWidth:
      return validInteger(value, 180, 360);
    case preferenceKeys.sidebarVisible:
    case preferenceKeys.tilingMode:
    case preferenceKeys.rightClickToPaste:
    case preferenceKeys.copyOnSelection:
    case preferenceKeys.checkUpdatesOnStartup:
    case preferenceKeys.terminalPasteWarningsDisabled:
      return value === "true" || value === "false";
    case preferenceKeys.terminalScrollbackLines:
      return validInteger(value, 0, 50000);
    case preferenceKeys.collapsedFolders: {
      let folders: unknown;
      try {
        folders = JSON.parse(value);
      } catch {
        return false;
      }
      return Array.isArray(folders)
        && folders.every((folder) => typeof folder === "string" && isNormalizedFolderPath(folder))
        && new Set(folders).size === folders.length;
    }
    case preferenceKeys.themeName:
      return ["warm", "classic", "moss", "fjord", "oled", "contrast"].includes(value);
    case preferenceKeys.terminalColors:
      return value === "follow" || value === "neutral";
    case preferenceKeys.uiSize:
      return value === "standard" || value === "large";
    case preferenceKeys.terminalFontSize:
      return validInteger(value, 8, 32);
    case preferenceKeys.interfaceScale:
      return ["0.8", "0.9", "1", "1.0", "1.1", "1.2", "1.3", "1.4", "1.5", "1.6"].includes(value);
    case preferenceKeys.terminalFontName:
      return ["system", "jetbrains", "menlo", "consolas", "dejavu"].includes(value);
    default:
      return false;
  }
}

/** Require a complete, exact set so restore never relies on permissive defaults. */
export function validateBackupPreferences(input: unknown): BackupPreferenceMap {
  if (!input || typeof input !== "object" || Array.isArray(input)) {
    throw new Error("Backup preferences must be an object.");
  }
  const entries = Object.entries(input);
  if (entries.length !== transferableKeys.length) {
    throw new Error("Backup preferences are incomplete or contain unsupported settings.");
  }

  const preferences: BackupPreferenceMap = {};
  for (const [key, value] of entries) {
    if (
      !transferableKeySet.has(key)
      || typeof value !== "string"
      || utf8Encoder.encode(value).byteLength > 64 * 1024
      || !isValidPreferenceValue(key, value)
    ) {
      throw new Error(`Backup preference ${key} is unsupported or invalid.`);
    }
    preferences[key] = value;
  }
  for (const key of transferableKeys) {
    if (!Object.hasOwn(preferences, key)) {
      throw new Error(`Backup preference ${key} is missing.`);
    }
  }
  return preferences;
}

/** Apply local preferences around the backend transaction and restore exact storage on failure. */
export async function applyBackupPreferences<T>(
  storage: BackupPreferenceStorage,
  imported: BackupPreferenceMap | null,
  operation: () => Promise<T>,
): Promise<T> {
  if (imported === null) {
    return operation();
  }
  const preferences = validateBackupPreferences(imported);
  const before = new Map<string, string | null>();
  for (const key of transferableKeys) {
    before.set(key, storage.getItem(key));
  }

  try {
    for (const key of transferableKeys) {
      storage.setItem(key, preferences[key]);
    }
    return await operation();
  } catch (cause) {
    const rollbackErrors: string[] = [];
    for (const key of transferableKeys) {
      try {
        const value = before.get(key) ?? null;
        if (value === null) {
          storage.removeItem(key);
        } else {
          storage.setItem(key, value);
        }
      } catch (rollbackError) {
        rollbackErrors.push(rollbackError instanceof Error ? rollbackError.message : String(rollbackError));
      }
    }
    if (rollbackErrors.length > 0) {
      const originalMessage = cause instanceof Error ? cause.message : String(cause);
      throw new Error(`${originalMessage}; preference rollback also failed: ${rollbackErrors.join("; ")}`, { cause });
    }
    throw cause;
  }
}
