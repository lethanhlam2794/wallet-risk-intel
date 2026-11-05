import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import App from "@/App";
import { AuthProvider } from "@/auth/auth-context";
import { buildPatch } from "@/features/providers/patch";
import type { FormFields } from "@/features/providers/patch";
import type {
  ProviderConfigPatch,
  ProviderConfigView,
} from "@/lib/types";
import {
  expectedPatchKeys,
  expectedPatchValues,
  genChains,
  genFormFields,
  genNetworks,
  genProvider,
  mulberry32,
} from "../helpers/gen";

const fetchMock = vi.fn<typeof fetch>();

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

const PROVIDER_CODES = [
  "alchemy",
  "blockscout",
  "ankr",
  "covalent",
  "moralis",
  "tatum",
  "bitquery",
  "oklink",
  "tokenview",
  "chainbase",
  "chainscan",
  "nodereal",
] as const;

const NETWORKS = genNetworks();

const BASE_VIEW: ProviderConfigView = {
  source_code: "chainscan",
  configured: true,
  api_key: { set: true, last4: "4242" },
  base_url: "https://api.chainscan.example",
  enabled_chains: ["ethereum"],
  status: "active",
  catalog_active: true,
  effective_active: true,
  requires_restart: [],
  updated_by: "ops",
  updated_at: "2026-11-04T23:00:00Z",
};

function renderApp(path: string) {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <AuthProvider>
        <App />
      </AuthProvider>
    </MemoryRouter>,
  );
}

function stubDetailFetch(view: ProviderConfigView) {
  fetchMock.mockImplementation(async (input, init) => {
    const url = String(input);
    if (url.endsWith("/api/v1/networks"))
      return json({ data: NETWORKS });
    if (url.includes("/changes"))
      return json({ data: { items: [], next_cursor: "" } });
    if ((init?.method ?? "GET").toUpperCase() === "PUT")
      return json({ data: view });
    return json({ data: view });
  });
}

beforeEach(() => {
  vi.stubGlobal("fetch", fetchMock);
  fetchMock.mockReset();
  sessionStorage.clear();
  localStorage.clear();
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("data-testid anchor contract", () => {
  it("login anchors exist, including login-error after a 401", async () => {
    fetchMock.mockImplementation(async () =>
      json({ error: { code: "UNAUTHORIZED", message: "bad key" } }, 401),
    );
    renderApp("/login");
    expect(screen.getByTestId("login-form")).toBeInTheDocument();
    expect(screen.getByTestId("login-username-input")).toBeInTheDocument();
    expect(screen.getByTestId("login-password-input")).toBeInTheDocument();
    expect(screen.getByTestId("login-submit")).toBeInTheDocument();
    await userEvent.type(screen.getByTestId("login-username-input"), "bad");
    await userEvent.type(screen.getByTestId("login-password-input"), "bad");
    await userEvent.click(screen.getByTestId("login-submit"));
    expect(await screen.findByTestId("login-error")).toBeInTheDocument();
  });

  it("shell + list anchors exist for an authed user", async () => {
    sessionStorage.setItem("cw.admin.token", "test-token");
    const views = PROVIDER_CODES.map((code) => ({
      ...BASE_VIEW,
      source_code: code,
      requires_restart: code === "alchemy" ? ["base_url"] : [],
    }));
    fetchMock.mockImplementation(async () => json({ data: views }));
    renderApp("/providers");
    expect(screen.getByTestId("app-header")).toBeInTheDocument();
    expect(screen.getByTestId("logout-button")).toBeInTheDocument();
    const table = await screen.findByTestId("providers-table");
    for (const code of PROVIDER_CODES)
      expect(
        within(table).getByTestId(`provider-row-${code}`),
      ).toBeInTheDocument();
    expect(
      screen.getByTestId("restart-badge-alchemy"),
    ).toBeInTheDocument();
  });

  it("detail/config/history anchors exist in their states", async () => {
    sessionStorage.setItem("cw.admin.token", "test-token");
    stubDetailFetch(BASE_VIEW);
    renderApp("/providers/chainscan");
    await screen.findByTestId("config-form");
    expect(screen.getByTestId("back-to-providers")).toBeInTheDocument();
    expect(screen.getByTestId("provider-tabs")).toBeInTheDocument();
    expect(screen.getByTestId("tab-config")).toBeInTheDocument();
    expect(screen.getByTestId("tab-history")).toBeInTheDocument();
    expect(screen.getByTestId("apikey-input")).toBeInTheDocument();
    expect(screen.getByTestId("baseurl-input")).toBeInTheDocument();
    expect(screen.getByTestId("baseurl-clear")).toBeInTheDocument();
    expect(screen.getByTestId("chains-fieldset")).toBeInTheDocument();
    for (const n of NETWORKS)
      expect(
        screen.getByTestId(`chain-check-${n.code}`),
      ).toBeInTheDocument();
    expect(screen.getByTestId("status-select")).toBeInTheDocument();
    expect(screen.getByTestId("save-button")).toBeInTheDocument();
    // history anchors
    await userEvent.click(screen.getByTestId("tab-history"));
    expect(await screen.findByTestId("history-table")).toBeInTheDocument();
    expect(await screen.findByTestId("history-empty")).toBeInTheDocument();
    // load-more only exists while next_cursor non-empty (contract)
  });

  it("warnings-region and field-error-base_url anchors appear on demand", async () => {
    sessionStorage.setItem("cw.admin.token", "test-token");
    fetchMock.mockImplementation(async (input, init) => {
      const url = String(input);
      if (url.endsWith("/api/v1/networks"))
        return json({ data: NETWORKS });
      if (url.includes("/changes"))
        return json({ data: { items: [], next_cursor: "" } });
      if ((init?.method ?? "GET").toUpperCase() === "PUT")
        return json({
          data: BASE_VIEW,
          warnings: [
            { code: "requires_restart", message: "restart needed" },
            { code: "unseen_code", message: "mystery" },
          ],
        });
      return json({ data: BASE_VIEW });
    });
    renderApp("/providers/chainscan");
    await screen.findByTestId("config-form");
    await userEvent.type(screen.getByTestId("apikey-input"), "new-key");
    await userEvent.click(screen.getByTestId("save-button"));
    expect(await screen.findByTestId("warnings-region")).toBeInTheDocument();
    expect(
      screen.getByTestId("warning-requires_restart"),
    ).toBeInTheDocument();
    expect(screen.getByTestId("warning-unseen_code")).toBeInTheDocument();
  });
});

describe("provider list invariants — 10 seeded iterations", () => {
  it("masked key, restart badge, configured badge hold for every generated row", async () => {
    sessionStorage.setItem("cw.admin.token", "test-token");
    let views: ProviderConfigView[] = [];
    fetchMock.mockImplementation(async () => json({ data: views }));
    for (let i = 0; i < 10; i++) {
      const rand = mulberry32(0xc0ffee + i);
      views = PROVIDER_CODES.map((code) => genProvider(rand, code));
      const view = renderApp("/providers");
      const table = await screen.findByTestId("providers-table");
      // wait for rows to land
      await screen.findByTestId(
        `provider-row-${views[0]!.source_code}`,
      );
      for (const v of views) {
        const row = within(table)
          .getByTestId(`provider-row-${v.source_code}`)
          .closest("tr")!;
        // masked key — the API-key cell contains ONLY the mask or
        // "not set"; a raw key (≥8 alnum) is never rendered
        const keyCell = row.querySelectorAll("td")[3]!;
        if (v.api_key.set)
          expect(keyCell.textContent).toBe(`••••${v.api_key.last4}`);
        else expect(keyCell.textContent).toBe("not set");
        expect(keyCell.textContent ?? "").not.toMatch(
          /[A-Za-z0-9]{8,}/,
        );
        // restart badge iff requires_restart non-empty
        const badge = screen.queryByTestId(
          `restart-badge-${v.source_code}`,
        );
        if (v.requires_restart.length > 0)
          expect(badge).toBeInTheDocument();
        else expect(badge).not.toBeInTheDocument();
        // configured badge iff configured === false
        if (v.configured) expect(row).toHaveTextContent("stored");
        else expect(row).toHaveTextContent("defaults/env");
        // chains rendering: null ⇒ env default, [] ⇒ none, else joined
        if (v.enabled_chains === null)
          expect(row).toHaveTextContent("env default");
        else if (v.enabled_chains.length === 0)
          expect(row).toHaveTextContent("none");
        else
          for (const c of v.enabled_chains)
            expect(row).toHaveTextContent(c);
      }
      view.unmount();
      fetchMock.mockClear();
    }
  });
});

describe("PATCH body property — buildPatch key-set", () => {
  it("15 seeded iterations: emitted keys equal dirty set after elision", () => {
    for (let i = 0; i < 15; i++) {
      const rand = mulberry32(0xbeef + i * 997);
      const stored = genProvider(rand, "chainscan");
      const fields = genFormFields(rand, stored);
      const patch = buildPatch(fields, stored);
      expect(Object.keys(patch).sort()).toEqual(
        expectedPatchKeys(fields, stored),
      );
      const expectedVals = expectedPatchValues(fields);
      for (const k of Object.keys(patch) as (keyof ProviderConfigPatch)[])
        expect(patch[k]).toEqual(expectedVals[k]);
    }
  });

  it("untouched fields always produce an empty patch", () => {
    const rand = mulberry32(1);
    for (let i = 0; i < 5; i++) {
      const stored = genProvider(rand, `p${i}`);
      const fields: FormFields = {
        apiKeyInput: "",
        baseUrl: { input: stored.base_url ?? "", cleared: false },
        chains: { kind: "untouched" },
        statusVal: stored.status,
      };
      expect(buildPatch(fields, stored)).toEqual({});
    }
  });
});

describe("PATCH body via real UI — seeded iterations", () => {
  it("captured PUT body key-set equals expected dirty set (4 iterations)", async () => {
    sessionStorage.setItem("cw.admin.token", "test-token");
    let stored: ProviderConfigView = BASE_VIEW;
    const putBodies: ProviderConfigPatch[] = [];
    fetchMock.mockImplementation(async (input, init) => {
      const url = String(input);
      if (url.endsWith("/api/v1/networks"))
        return json({ data: NETWORKS });
      if (url.includes("/changes"))
        return json({ data: { items: [], next_cursor: "" } });
      if ((init?.method ?? "GET").toUpperCase() === "PUT") {
        putBodies.push(JSON.parse(String(init?.body)) as ProviderConfigPatch);
        return json({ data: stored });
      }
      return json({ data: stored });
    });

    for (let i = 0; i < 4; i++) {
      const rand = mulberry32(0xf00d + i * 31337);
      const gen = genProvider(rand, "chainscan");
      // force base_url non-null so the Clear button is exercisable
      stored = { ...gen, base_url: gen.base_url ?? "https://stored.example.com" };
      const fields = genFormFields(rand, stored);
      const expected = expectedPatchKeys(fields, stored);
      const view = renderApp("/providers/chainscan");
      await screen.findByTestId("config-form");
      await driveForm(fields, stored);
      const save = screen.getByTestId("save-button");
      if (expected.length === 0) {
        expect(save).toBeDisabled();
      } else {
        await userEvent.click(save);
        await waitFor(() =>
          expect(putBodies).toHaveLength(i + 1),
        );
        expect(
          Object.keys(putBodies[i]!).sort(),
        ).toEqual(expected);
      }
      view.unmount();
    }
  });
});

async function driveForm(
  fields: FormFields,
  stored: ProviderConfigView,
) {
  if (fields.apiKeyInput !== "")
    await userEvent.type(
      screen.getByTestId("apikey-input"),
      fields.apiKeyInput,
    );

  if (fields.baseUrl.cleared) {
    await userEvent.click(screen.getByTestId("baseurl-clear"));
    const dialog = await screen.findByRole("dialog");
    await userEvent.click(
      within(dialog).getByRole("button", { name: /^clear$/i }),
    );
  } else if (fields.baseUrl.input !== (stored.base_url ?? "")) {
    const input = screen.getByTestId("baseurl-input");
    await userEvent.clear(input);
    if (fields.baseUrl.input !== "")
      await userEvent.type(input, fields.baseUrl.input);
  }

  if (fields.chains.kind === "set") {
    const want = new Set(fields.chains.value);
    const current = new Set(stored.enabled_chains ?? []);
    const adds = NETWORKS.filter(
      (n) => want.has(n.code) && !current.has(n.code),
    );
    const removes = NETWORKS.filter(
      (n) => !want.has(n.code) && current.has(n.code),
    );
    if (want.size === 0 && current.size === 0) {
      // all already unchecked — toggle one on then off to reach []
      const cb = screen.getByTestId(`chain-check-${NETWORKS[0]!.code}`);
      await userEvent.click(cb);
      await userEvent.click(cb);
      const dialog = await screen.findByRole("dialog");
      await userEvent.click(
        within(dialog).getByRole("button", {
          name: /disable everywhere/i,
        }),
      );
    } else {
      for (const n of adds)
        await userEvent.click(
          screen.getByTestId(`chain-check-${n.code}`),
        );
      for (const n of removes)
        await userEvent.click(
          screen.getByTestId(`chain-check-${n.code}`),
        );
      if (want.size === 0) {
        const dialog = await screen.findByRole("dialog");
        await userEvent.click(
          within(dialog).getByRole("button", {
            name: /disable everywhere/i,
          }),
        );
      }
    }
  }

  if (fields.statusVal !== stored.status) {
    await userEvent.click(screen.getByTestId("status-select"));
    await userEvent.click(
      await screen.findByRole("option", {
        name:
          fields.statusVal === "active"
            ? /^active$/i
            : /^inactive$/i,
      }),
    );
  }
}

describe("tab-switch persistence", () => {
  it("seeded: dirty Config + accumulated History survive Config→History→Config→History", async () => {
    sessionStorage.setItem("cw.admin.token", "test-token");
    const rand = mulberry32(0xabcd);
    const stored: ProviderConfigView = {
      ...genProvider(rand, "chainscan"),
      base_url: "https://stored.example.com",
      enabled_chains: genChains(rand) ?? ["ethereum"],
    };
    const page = {
      items: [
        {
          id: "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa",
          field: "status",
          previous_value: "active",
          new_value: "inactive",
          actor_id: "ops",
          actor_type: "api_client",
          changed_at: "2026-11-04T20:00:00Z",
        },
      ],
      next_cursor: "",
    };
    fetchMock.mockImplementation(async (input) => {
      const url = String(input);
      if (url.endsWith("/api/v1/networks"))
        return json({ data: NETWORKS });
      if (url.includes("/changes")) return json({ data: page });
      return json({ data: stored });
    });
    renderApp("/providers/chainscan");
    const input = await screen.findByTestId("baseurl-input");
    await userEvent.clear(input);
    await userEvent.type(input, "https://dirty.example.com");
    await userEvent.click(screen.getByTestId("tab-history"));
    const table = await screen.findByTestId("history-table");
    await within(table).findByText("status");
    await userEvent.click(screen.getByTestId("tab-config"));
    expect(screen.getByTestId("baseurl-input")).toHaveValue(
      "https://dirty.example.com",
    );
    await userEvent.click(screen.getByTestId("tab-history"));
    expect(within(table).getAllByRole("row")).toHaveLength(2);
  });
});
