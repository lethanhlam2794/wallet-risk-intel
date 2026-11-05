import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ApiError, apiFetch, onUnauthorized } from "@/lib/api";

const fetchMock = vi.fn<typeof fetch>();

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

beforeEach(() => {
  vi.stubGlobal("fetch", fetchMock);
  fetchMock.mockReset();
});

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("apiFetch envelope handling", () => {
  it("unwraps the {data} envelope", async () => {
    fetchMock.mockResolvedValue(jsonResponse({ data: [1, 2, 3] }));
    const res = await apiFetch<number[]>("/api/v1/things");
    expect(res.data).toEqual([1, 2, 3]);
  });

  it("passes warnings through on {data, warnings} envelopes", async () => {
    fetchMock.mockResolvedValue(
      jsonResponse({
        data: { ok: true },
        warnings: [{ code: "requires_restart", message: "restart" }],
      }),
    );
    const res = await apiFetch("/api/v1/x", {
      method: "PUT",
      body: { status: "active" },
    });
    expect(res.data).toEqual({ ok: true });
    expect(res.warnings).toEqual([
      { code: "requires_restart", message: "restart" },
    ]);
  });

  it("throws ApiError with mapped fields on an error envelope", async () => {
    fetchMock.mockResolvedValue(
      jsonResponse(
        {
          error: {
            code: "BAD_REQUEST",
            message: "nope",
            request_id: "req-1",
          },
        },
        400,
      ),
    );
    const err: unknown = await apiFetch("/api/v1/x").catch((e) => e);
    expect(err).toBeInstanceOf(ApiError);
    const apiErr = err as ApiError;
    expect(apiErr.status).toBe(400);
    expect(apiErr.code).toBe("BAD_REQUEST");
    expect(apiErr.message).toBe("nope");
    expect(apiErr.requestId).toBe("req-1");
  });

  it("maps error.allowed_hosts on 400", async () => {
    fetchMock.mockResolvedValue(
      jsonResponse(
        {
          error: {
            code: "BAD_REQUEST",
            message: "host not allowed",
            allowed_hosts: ["api.etherscan.io", "api.bscscan.com"],
          },
        },
        400,
      ),
    );
    const err: unknown = await apiFetch("/api/v1/x").catch((e) => e);
    expect(err).toBeInstanceOf(ApiError);
    expect((err as ApiError).allowedHosts).toEqual([
      "api.etherscan.io",
      "api.bscscan.com",
    ]);
  });

  it("throws ApiError on non-2xx without an error body", async () => {
    fetchMock.mockResolvedValue(new Response("boom", { status: 500 }));
    const err: unknown = await apiFetch("/api/v1/x").catch((e) => e);
    expect(err).toBeInstanceOf(ApiError);
    expect((err as ApiError).status).toBe(500);
  });

  it("propagates network failures as-is (not ApiError)", async () => {
    fetchMock.mockRejectedValue(new TypeError("fetch failed"));
    const err: unknown = await apiFetch("/api/v1/x").catch((e) => e);
    expect(err).toBeInstanceOf(TypeError);
    expect(err).not.toBeInstanceOf(ApiError);
  });
});

describe("apiFetch credentials and transport", () => {
  it("sends the token only as Bearer — never URL, body, or X-API-Key", async () => {
    fetchMock.mockResolvedValue(jsonResponse({ data: {} }));
    const token = "adm-test-token-123";
    await apiFetch("/api/v1/admin/provider-configs", {
      token,
      method: "PUT",
      body: { status: "active" },
    });
    const call = fetchMock.mock.calls[0]!;
    const url = String(call[0]);
    const init = call[1]!;
    const headers = new Headers(init.headers);
    expect(url).toBe("/api/v1/admin/provider-configs");
    expect(url).not.toContain(token);
    expect(headers.get("Authorization")).toBe(`Bearer ${token}`);
    expect(headers.get("X-API-Key")).toBeNull();
    expect(String(init.body)).not.toContain(token);
    expect(String(init.body)).toContain("active");
  });

  it("bounds every request with AbortSignal.timeout(15000)", async () => {
    const timeoutSpy = vi.spyOn(AbortSignal, "timeout");
    fetchMock.mockResolvedValue(jsonResponse({ data: 1 }));
    await apiFetch("/api/v1/x");
    expect(timeoutSpy).toHaveBeenCalledWith(15_000);
    const init = fetchMock.mock.calls[0]![1]!;
    expect(init.signal).toBeInstanceOf(AbortSignal);
  });
});

describe("apiFetch 401 handling", () => {
  it("invokes the registered unauthorized handler on 401", async () => {
    fetchMock.mockResolvedValue(
      jsonResponse(
        { error: { code: "UNAUTHORIZED", message: "bad key" } },
        401,
      ),
    );
    const cb = vi.fn();
    const off = onUnauthorized(cb);
    await apiFetch("/api/v1/x").catch(() => {});
    expect(cb).toHaveBeenCalledTimes(1);
    off();
  });

  it("skips the handler when skipUnauthorizedHandler is set (login probe)", async () => {
    fetchMock.mockResolvedValue(
      jsonResponse(
        { error: { code: "UNAUTHORIZED", message: "bad key" } },
        401,
      ),
    );
    const cb = vi.fn();
    const off = onUnauthorized(cb);
    const err: unknown = await apiFetch("/api/v1/x", {
      skipUnauthorizedHandler: true,
    }).catch((e) => e);
    expect(cb).not.toHaveBeenCalled();
    expect((err as ApiError).status).toBe(401);
    off();
  });

  it("is single-slot: a later registration replaces the earlier one", async () => {
    fetchMock.mockResolvedValue(
      jsonResponse({ error: { code: "U", message: "x" } }, 401),
    );
    const first = vi.fn();
    const second = vi.fn();
    const offFirst = onUnauthorized(first);
    const offSecond = onUnauthorized(second);
    await apiFetch("/api/v1/x").catch(() => {});
    expect(first).not.toHaveBeenCalled();
    expect(second).toHaveBeenCalledTimes(1);
    offSecond();
    offFirst();
  });
});
