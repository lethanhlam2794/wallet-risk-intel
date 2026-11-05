import { getToken } from "./auth";
import type { Envelope } from "./types";

const REQUEST_TIMEOUT_MS = 15_000;

export class ApiError extends Error {
  readonly status: number;
  readonly code: string;
  readonly allowedHosts: string[] | undefined;
  readonly requestId: string | undefined;

  constructor(init: {
    status: number;
    code: string;
    message: string;
    allowedHosts?: string[];
    requestId?: string;
  }) {
    super(init.message);
    this.name = "ApiError";
    this.status = init.status;
    this.code = init.code;
    this.allowedHosts = init.allowedHosts;
    this.requestId = init.requestId;
  }
}

// Single-slot 401 subscriber. auth-context registers the handler that
// drops the session token and redirects to /login?reason=session_expired.
// The login call passes skipUnauthorizedHandler so bad credentials
// produce an inline error instead of a session-expired redirect (A1).
type UnauthorizedHandler = () => void;
let unauthorizedHandler: UnauthorizedHandler | null = null;

export function onUnauthorized(cb: UnauthorizedHandler): () => void {
  unauthorizedHandler = cb;
  return () => {
    if (unauthorizedHandler === cb) unauthorizedHandler = null;
  };
}

export interface ApiFetchOptions {
  token?: string | null;
  method?: string;
  body?: unknown;
  signal?: AbortSignal;
  skipUnauthorizedHandler?: boolean;
}

interface ErrorBody {
  code?: unknown;
  message?: unknown;
  allowed_hosts?: unknown;
  request_id?: unknown;
}

function isErrorEnvelope(v: unknown): v is { error: ErrorBody } {
  return (
    typeof v === "object" &&
    v !== null &&
    "error" in v &&
    typeof (v as { error: unknown }).error === "object" &&
    (v as { error: unknown }).error !== null
  );
}

function parseJson(text: string): unknown {
  try {
    return JSON.parse(text);
  } catch {
    return null;
  }
}

export async function apiFetch<T>(
  path: string,
  options: ApiFetchOptions = {},
): Promise<Envelope<T>> {
  const {
    method = "GET",
    body,
    signal,
    skipUnauthorizedHandler = false,
  } = options;
  const token = options.token === undefined ? getToken() : options.token;

  const headers: Record<string, string> = {};
  if (token) headers["Authorization"] = `Bearer ${token}`;

  const init: RequestInit = { method, headers };
  const sendBody = body !== undefined && method !== "GET" && method !== "HEAD";
  if (sendBody) {
    headers["Content-Type"] = "application/json";
    init.body = JSON.stringify(body);
  }

  const timeoutSignal = AbortSignal.timeout(REQUEST_TIMEOUT_MS);
  init.signal = signal
    ? AbortSignal.any([signal, timeoutSignal])
    : timeoutSignal;

  const res = await fetch(path, init);

  if (res.status === 401 && !skipUnauthorizedHandler) {
    unauthorizedHandler?.();
  }

  const text = await res.text();
  const payload = text ? parseJson(text) : null;

  if (!res.ok) {
    const error = isErrorEnvelope(payload) ? payload.error : null;
    throw new ApiError({
      status: res.status,
      code:
        typeof error?.code === "string" ? error.code : `HTTP_${res.status}`,
      message:
        typeof error?.message === "string"
          ? error.message
          : `Request failed with status ${res.status}`,
      allowedHosts: Array.isArray(error?.allowed_hosts)
        ? (error.allowed_hosts as string[])
        : undefined,
      requestId:
        typeof error?.request_id === "string" ? error.request_id : undefined,
    });
  }

  return (payload ?? { data: undefined }) as Envelope<T>;
}
