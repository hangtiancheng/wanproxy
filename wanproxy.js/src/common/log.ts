/**
 * Leveled logging with C++-style component paths, mirroring the Go
 * common/log.go helper (built on log/slog) and the C++ Log level masks.
 *
 * Output is a single `key=value` line per record, written through a
 * configurable sink (stderr by default) so tests can capture it.
 */

/** Log levels supported by the C++ log-mask configuration. */
export type LogLevel = "debug" | "info" | "warn" | "error";

/** Ordered list of log levels, least to most severe. */
export const LOG_LEVELS: readonly LogLevel[] = [
  "debug",
  "info",
  "warn",
  "error",
];

const LEVEL_RANK: Record<LogLevel, number> = {
  debug: 10,
  info: 20,
  warn: 30,
  error: 40,
};

/** LogFields carries structured key/value pairs attached to a record. */
export interface LogFields {
  readonly [key: string]: unknown;
}

/** LogSink receives one fully formatted log line. */
export type LogSink = (line: string) => void;

let globalLevel: LogLevel = "info";
let globalSink: LogSink = (line) => {
  process.stderr.write(`${line}\n`);
};

/** setLogLevel sets the global log level, like Log::mask(".?", level). */
export function setLogLevel(level: LogLevel): void {
  globalLevel = level;
}

/** getLogLevel returns the current global log level. */
export function getLogLevel(): LogLevel {
  return globalLevel;
}

/** setLogSink redirects formatted log lines (used by tests and daemons). */
export function setLogSink(sink: LogSink): void {
  globalSink = sink;
}

/** restoreDefaultLogSink resets the sink to stderr output. */
export function restoreDefaultLogSink(): void {
  globalSink = (line) => {
    process.stderr.write(`${line}\n`);
  };
}

/**
 * parseLogLevel maps the C++ log level names onto a LogLevel.  It throws
 * for unknown names, mirroring common.ParseLogLevel.
 */
export function parseLogLevel(name: string): LogLevel {
  switch (name.toUpperCase()) {
    case "DEBUG":
      return "debug";
    case "INFO":
    case "NOTICE":
      return "info";
    case "WARNING":
    case "ERR":
    case "CRIT":
    case "ALERT":
    case "EMERG":
      return "warn";
    default:
      throw new Error(`unknown log level "${name}"`);
  }
}

/** Logger is a leveled logger optionally tagged with a component path. */
export interface Logger {
  /** The C++-style component path, e.g. "/wanproxy/proxy/proxy0". */
  readonly component: string | undefined;
  debug(message: string, fields?: LogFields): void;
  info(message: string, fields?: LogFields): void;
  warn(message: string, fields?: LogFields): void;
  error(message: string, fields?: LogFields): void;
  /** withComponent returns a child logger tagged with a component path. */
  withComponent(path: string): Logger;
}

class ComponentLogger implements Logger {
  public constructor(public readonly component: string | undefined) {}

  public debug(message: string, fields?: LogFields): void {
    this.log("debug", message, fields);
  }

  public info(message: string, fields?: LogFields): void {
    this.log("info", message, fields);
  }

  public warn(message: string, fields?: LogFields): void {
    this.log("warn", message, fields);
  }

  public error(message: string, fields?: LogFields): void {
    this.log("error", message, fields);
  }

  public withComponent(path: string): Logger {
    return new ComponentLogger(path);
  }

  private log(level: LogLevel, message: string, fields?: LogFields): void {
    if (LEVEL_RANK[level] < LEVEL_RANK[globalLevel]) {
      return;
    }
    globalSink(formatLogLine(level, this.component, message, fields));
  }
}

/** createLogger returns a logger, optionally tagged with a component. */
export function createLogger(component?: string): Logger {
  return new ComponentLogger(component);
}

/** component returns a logger tagged with a C++-style component path. */
export function component(path: string): Logger {
  return new ComponentLogger(path);
}

/** formatLogLine renders one record as a deterministic key=value line. */
export function formatLogLine(
  level: LogLevel,
  componentPath: string | undefined,
  message: string,
  fields?: LogFields,
): string {
  const parts: string[] = [`level=${level.toUpperCase()}`];
  if (componentPath !== undefined) {
    parts.push(`component=${formatScalar(componentPath)}`);
  }
  parts.push(`msg=${JSON.stringify(message)}`);
  if (fields !== undefined) {
    for (const [key, value] of Object.entries(fields)) {
      parts.push(`${key}=${formatScalar(value)}`);
    }
  }
  return parts.join(" ");
}

function formatScalar(value: unknown): string {
  if (typeof value === "string") {
    if (value === "" || /[\s"=]/.test(value)) {
      return JSON.stringify(value);
    }
    return value;
  }
  if (value instanceof Error) {
    return JSON.stringify(value.message);
  }
  return String(value);
}
