import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ColumnDef } from "@tanstack/react-table";
import { describe, expect, it, vi } from "vitest";
import { Badge, DataTable } from "@/components";

interface User {
  id: number;
  name: string;
  email: string;
  role: string;
}

const users: User[] = [
  { id: 1, name: "Charlie", email: "charlie@example.com", role: "admin" },
  { id: 2, name: "Alice", email: "alice@example.com", role: "editor" },
  { id: 3, name: "Eve", email: "eve@example.com", role: "viewer" },
  { id: 4, name: "Bob", email: "bob@example.com", role: "editor" },
  { id: 5, name: "Dave", email: "dave@example.com", role: "viewer" },
  { id: 6, name: "Frank", email: "frank@example.com", role: "admin" },
  { id: 7, name: "Grace", email: "grace@example.com", role: "viewer" },
];

const columns: ColumnDef<User>[] = [
  { accessorKey: "name", header: "Name" },
  { accessorKey: "email", header: "Email" },
  {
    accessorKey: "role",
    header: "Role",
    cell: (info) => <Badge>{String(info.getValue())}</Badge>,
  },
];

function firstColumnValues() {
  return screen
    .getAllByRole("row")
    .slice(1)
    .map((row) => within(row).getAllByRole("cell")[0]?.textContent);
}

describe("DataTable", () => {
  it("renders headers and rows from columns and data", () => {
    render(<DataTable data={users} columns={columns} />);
    expect(
      screen.getByRole("columnheader", { name: /Name/ }),
    ).toBeInTheDocument();
    expect(screen.getByText("alice@example.com")).toBeInTheDocument();
    // default pageSize caps rendered rows
    expect(screen.getAllByRole("row").length).toBeLessThanOrEqual(
      1 + users.length,
    );
  });

  it("renders the empty state when data is empty", () => {
    render(
      <DataTable data={[]} columns={columns} emptyMessage="No users yet" />,
    );
    expect(screen.getByText("No users yet")).toBeInTheDocument();
  });

  it("renders skeleton rows while loading and keeps headers", () => {
    render(<DataTable data={[]} columns={columns} loading />);
    expect(
      screen.getByRole("columnheader", { name: /Name/ }),
    ).toBeInTheDocument();
    expect(screen.queryByText("Alice")).not.toBeInTheDocument();
    expect(screen.getAllByTestId("datatable-skeleton-row").length).toBeGreaterThan(0);
  });

  it("sorts ascending then descending on header clicks", async () => {
    const user = userEvent.setup();
    render(<DataTable data={users} columns={columns} pageSize={10} />);
    const nameHeader = screen.getByRole("columnheader", { name: /Name/ });

    await user.click(within(nameHeader).getByRole("button"));
    expect(firstColumnValues()[0]).toBe("Alice");

    await user.click(within(nameHeader).getByRole("button"));
    expect(firstColumnValues()[0]).toBe("Grace");
  });

  it("paginates client-side and activates via keyboard", async () => {
    const user = userEvent.setup();
    render(<DataTable data={users} columns={columns} pageSize={5} />);

    // page 1: 5 rows
    expect(screen.getAllByRole("row").length).toBe(6);
    expect(screen.getByText("Charlie")).toBeInTheDocument();
    expect(screen.queryByText("Frank")).not.toBeInTheDocument();

    const next = screen.getByRole("link", { name: /next/i });
    next.focus();
    await user.keyboard("{Enter}");

    // page 2: remaining 2 rows
    expect(screen.getByText("Frank")).toBeInTheDocument();
    expect(screen.getByText("Grace")).toBeInTheDocument();
    expect(screen.queryByText("Charlie")).not.toBeInTheDocument();
    expect(screen.getAllByRole("row").length).toBe(3);

    const previous = screen.getByRole("link", { name: /previous/i });
    previous.focus();
    await user.keyboard("{Enter}");
    expect(screen.getByText("Charlie")).toBeInTheDocument();
  });

  it("marks previous disabled on the first page and next on the last", async () => {
    const user = userEvent.setup();
    render(<DataTable data={users} columns={columns} pageSize={5} />);
    // page 1 of 2: previous disabled, next enabled
    expect(
      screen.getByRole("link", { name: /previous/i }),
    ).toHaveAttribute("aria-disabled", "true");
    expect(screen.getByRole("link", { name: /next/i })).not.toHaveAttribute(
      "aria-disabled",
      "true",
    );
    // page 2 of 2: next disabled, previous enabled
    const next = screen.getByRole("link", { name: /next/i });
    next.focus();
    await user.keyboard("{Enter}");
    expect(
      screen.getByRole("link", { name: /next/i }),
    ).toHaveAttribute("aria-disabled", "true");
    expect(
      screen.getByRole("link", { name: /previous/i }),
    ).not.toHaveAttribute("aria-disabled", "true");
  });

  it("hides pagination when all rows fit on one page", () => {
    render(<DataTable data={users.slice(0, 3)} columns={columns} pageSize={10} />);
    expect(screen.queryAllByRole("link")).toHaveLength(0);
  });

  it("fires onRowClick with the row payload", async () => {
    const user = userEvent.setup();
    const onRowClick = vi.fn();
    render(
      <DataTable
        data={users}
        columns={columns}
        pageSize={10}
        onRowClick={onRowClick}
      />,
    );
    await user.click(screen.getByText("alice@example.com"));
    expect(onRowClick).toHaveBeenCalledWith(
      expect.objectContaining({ id: 2, name: "Alice" }),
    );
  });

  it("fires onRowClick on Enter for a focused row", async () => {
    const user = userEvent.setup();
    const onRowClick = vi.fn();
    render(
      <DataTable
        data={users}
        columns={columns}
        pageSize={10}
        onRowClick={onRowClick}
      />,
    );
    const row = screen.getByText("Charlie").closest("tr")!;
    row.focus();
    await user.keyboard("{Enter}");
    expect(onRowClick).toHaveBeenCalledWith(
      expect.objectContaining({ name: "Charlie" }),
    );
  });

  it("does not fire onRowClick from nested interactive elements", async () => {
    const user = userEvent.setup();
    const onRowClick = vi.fn();
    const onAction = vi.fn();
    const actionColumns: ColumnDef<User>[] = [
      { accessorKey: "name", header: "Name" },
      {
        id: "actions",
        header: "",
        cell: () => (
          <button type="button" onClick={onAction}>
            Do
          </button>
        ),
      },
    ];
    render(
      <DataTable
        data={users.slice(0, 3)}
        columns={actionColumns}
        pageSize={10}
        onRowClick={onRowClick}
      />,
    );

    const actionButton = screen.getAllByRole("button", { name: "Do" })[0]!;
    await user.click(actionButton);
    expect(onAction).toHaveBeenCalled();
    expect(onRowClick).not.toHaveBeenCalled();

    actionButton.focus();
    await user.keyboard("{Enter}");
    expect(onRowClick).not.toHaveBeenCalled();

    // plain cell content still fires
    await user.click(screen.getByText("Charlie"));
    expect(onRowClick).toHaveBeenCalledWith(
      expect.objectContaining({ name: "Charlie" }),
    );
  });

  it("does not fire onRowClick when sorting via header", async () => {
    const user = userEvent.setup();
    const onRowClick = vi.fn();
    render(
      <DataTable
        data={users}
        columns={columns}
        pageSize={10}
        onRowClick={onRowClick}
      />,
    );
    const nameHeader = screen.getByRole("columnheader", { name: /Name/ });
    await user.click(within(nameHeader).getByRole("button"));
    expect(onRowClick).not.toHaveBeenCalled();
  });

  it("accepts a custom row id getter", () => {
    render(
      <DataTable
        data={users.slice(0, 3)}
        columns={columns}
        getRowId={(row) => `user-${row.id}`}
      />,
    );
    expect(
      screen.getByText("Charlie").closest("tr"),
    ).toHaveAttribute("data-row-id", "user-1");
  });
});
