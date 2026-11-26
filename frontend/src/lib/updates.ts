export interface UpdateRelease {
  version: string;
  releaseURL: string;
}

export interface UpdateCheckResult extends UpdateRelease {
  automatic: boolean;
  checked: boolean;
  error: string;
}

export function canCheckForUpdates(availability: string | undefined): boolean {
  return availability === "supported" || availability === "package-manager";
}

export function githubReleaseURL(value: unknown): string {
  if (typeof value !== "string") return "";
  try {
    const url = new URL(value);
    return url.origin === "https://github.com" && url.pathname.startsWith("/sshbrowse/sshbrowse/releases/tag/") && !url.username && !url.password
      ? url.href : "";
  } catch {
    return "";
  }
}
