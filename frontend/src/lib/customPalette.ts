import type { ITheme } from "@xterm/xterm";

export interface CustomPalette {
  surface: string;
  accent: string;
  terminal: string;
}

export const defaultCustomPalette: CustomPalette = {
  surface: "#15191e",
  accent: "#c9ae79",
  terminal: "#101418",
};

export function isHexColor(value: unknown): value is string {
  return typeof value === "string" && /^#[0-9a-f]{6}$/i.test(value);
}

export function isCustomPalette(value: unknown): value is CustomPalette {
  if (!value || typeof value !== "object" || Array.isArray(value)) return false;
  const entries = Object.entries(value);
  return entries.length === 3
    && entries.every(([key, color]) => ["surface", "accent", "terminal"].includes(key) && isHexColor(color));
}

export function parseCustomPalette(value: string | null): CustomPalette {
  try {
    const parsed: unknown = JSON.parse(value ?? "null");
    if (isCustomPalette(parsed)) {
      return { surface: parsed.surface.toLowerCase(), accent: parsed.accent.toLowerCase(), terminal: parsed.terminal.toLowerCase() };
    }
  } catch {
    // A missing or damaged optional preference uses the default palette.
  }
  return { ...defaultCustomPalette };
}

function channels(color: string): number[] {
  return [1, 3, 5].map((offset) => parseInt(color.slice(offset, offset + 2), 16));
}

function mix(color: string, target: string, amount: number): string {
  const end = channels(target);
  return "#" + channels(color).map((channel, index) =>
    Math.round(channel + (end[index] - channel) * amount).toString(16).padStart(2, "0"),
  ).join("");
}

function luminance(color: string): number {
  const values = channels(color).map((channel) => {
    const value = channel / 255;
    return value <= 0.04045 ? value / 12.92 : ((value + 0.055) / 1.055) ** 2.4;
  });
  return values[0] * 0.2126 + values[1] * 0.7152 + values[2] * 0.0722;
}

export function contrastRatio(first: string, second: string): number {
  const a = luminance(first);
  const b = luminance(second);
  return (Math.max(a, b) + 0.05) / (Math.min(a, b) + 0.05);
}

function readable(color: string, background: string, minimum = 4.5): string {
  if (contrastRatio(color, background) >= minimum) return color;
  const target = contrastRatio("#ffffff", background) > contrastRatio("#000000", background) ? "#ffffff" : "#000000";
  for (let step = 1; step <= 100; step++) {
    const adjusted = mix(color, target, step / 100);
    if (contrastRatio(adjusted, background) >= minimum) return adjusted;
  }
  return target;
}

export function customTerminalTheme(palette: CustomPalette): ITheme {
  const background = palette.terminal;
  const foreground = readable("#e5e9ed", background, 7);
  return {
    background, foreground,
    cursor: readable(palette.accent, background), cursorAccent: background,
    selectionBackground: readable(mix(background, foreground, 0.24), foreground),
    selectionForeground: foreground,
    // Keep ANSI black suitable for backgrounds; xterm adjusts low-contrast glyphs.
    black: "#15191d",
    red: readable("#e59b99", background), green: readable("#a2c9a7", background),
    yellow: readable("#dcc88c", background), blue: readable("#9cbcd7", background),
    magenta: readable("#c8afd2", background), cyan: readable("#9dcdcc", background),
    white: readable("#e5e9ed", background), brightBlack: readable("#8d989f", background),
    brightRed: readable("#f0abab", background), brightGreen: readable("#b9dcbb", background),
    brightYellow: readable("#ead9a5", background), brightBlue: readable("#b8d2e6", background),
    brightMagenta: readable("#dac5e1", background), brightCyan: readable("#b8dfdd", background),
    brightWhite: readable("#ffffff", background),
  };
}

/** Supply the same semantic roles as the built-in themes, including native chrome. */
export function customThemeProperties(palette: CustomPalette): Record<string, string> {
  const background = palette.surface;
  const text = readable("#e5e9ed", background, 7);
  let muted = readable("#9aa6b2", background);
  const raised = readable(mix(background, text, 0.06), text);
  const hover = readable(mix(background, text, 0.1), text);
  const border = mix(background, text, 0.2);
  const subtle = mix(background, text, 0.1);
  const accent = readable(palette.accent, background, 3);
  const selection = readable(mix(background, accent, 0.16), text);
  const input = readable(mix(background, text === "#000000" ? "#ffffff" : "#000000", 0.16), text);
  for (let step = 0; step <= 100; step++) {
    const candidate = mix(muted, text, step / 100);
    if ([background, raised, hover, selection, input].every((surface) => contrastRatio(candidate, surface) >= 4.5)) {
      muted = candidate;
      break;
    }
  }
  const error = readable("#e3908d", raised);
  const errorForeground = readable("#21191a", error);
  const terminal = customTerminalTheme(palette);
  return {
    "canvas": background, "chrome": background, "chrome-foreground": text, "chrome-border": subtle,
    "toolbar": background, "toolbar-foreground": muted, "toolbar-border": subtle,
    "toolbar-control": raised, "toolbar-control-foreground": text, "toolbar-control-hover": hover,
    "surface": background, "surface-raised": raised, "surface-overlay": raised,
    "overlay-backdrop": "rgb(0 0 0 / 66%)", "overlay-backdrop-soft": "rgb(0 0 0 / 48%)",
    "input-surface": input,
    "text-primary": text, "text-secondary": muted, "text-muted": muted, "text-disabled": muted,
    "text-placeholder": muted, "icon-muted": muted, "control-hover": hover,
    "selection": selection, "selection-inactive": raised, "border": border, "border-subtle": subtle,
    "separator": border, "focus-ring": accent, "accent": accent,
    "accent-foreground": readable("#15191d", accent), "accent-subtle": selection,
    "accent-hover": mix(accent, text, 0.12), "sidebar": background, "sidebar-foreground": text,
    "sidebar-muted-foreground": muted, "sidebar-control-surface": raised, "sidebar-row-hover": hover,
    "sidebar-row-active": selection, "sidebar-row-selected": raised, "sidebar-border": subtle,
    "terminal-background": palette.terminal, "terminal-foreground": terminal.foreground!,
    "status-warning": readable("#dfb871", raised), "status-warning-surface": raised,
    "status-error": error, "status-error-foreground": errorForeground,
    "status-error-hover": readable(mix(error, text, 0.12), errorForeground), "status-error-surface": raised,
  };
}

export function customThemeStyle(palette: CustomPalette, neutral = false): string {
  const properties = customThemeProperties(palette);
  if (neutral) {
    properties["terminal-background"] = "#000000";
    properties["terminal-foreground"] = "#e5e7e9";
  }
  return Object.entries(properties).map(([key, value]) => `--${key}: ${value}`).join("; ");
}

export function applyCustomTheme(element: HTMLElement, palette: CustomPalette, enabled: boolean, neutral: boolean): void {
  const properties = customThemeProperties(palette);
  if (neutral) {
    properties["terminal-background"] = "#000000";
    properties["terminal-foreground"] = "#e5e7e9";
  }
  for (const [key, value] of Object.entries(properties)) {
    if (enabled) element.style.setProperty(`--${key}`, value);
    else element.style.removeProperty(`--${key}`);
  }
  element.style.colorScheme = enabled ? (luminance(palette.surface) > 0.179 ? "light" : "dark") : "";
}
