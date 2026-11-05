import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { Button, DropdownMenu } from "@/components";

const items = [
  { label: "Edit", onSelect: vi.fn() },
  { label: "Disabled action", disabled: true, onSelect: vi.fn() },
  { label: "Delete", destructive: true, onSelect: vi.fn() },
];

describe("DropdownMenu", () => {
  it("opens on trigger click and shows items", async () => {
    const user = userEvent.setup();
    render(
      <DropdownMenu trigger={<Button>Actions</Button>} items={items} />,
    );
    await user.click(screen.getByRole("button", { name: "Actions" }));
    expect(await screen.findByRole("menuitem", { name: "Edit" })).toBeVisible();
    expect(
      screen.getByRole("menuitem", { name: "Delete" }),
    ).toBeInTheDocument();
  });

  it("fires onSelect when an item is clicked", async () => {
    const user = userEvent.setup();
    const onSelect = vi.fn();
    render(
      <DropdownMenu
        trigger={<Button>Actions</Button>}
        items={[{ label: "Edit", onSelect }]}
      />,
    );
    await user.click(screen.getByRole("button", { name: "Actions" }));
    await user.click(await screen.findByRole("menuitem", { name: "Edit" }));
    expect(onSelect).toHaveBeenCalledTimes(1);
  });

  it("supports keyboard selection with arrows and Enter", async () => {
    const user = userEvent.setup();
    const onSelect = vi.fn();
    render(
      <DropdownMenu
        trigger={<Button>Actions</Button>}
        items={[
          { label: "First", onSelect },
          { label: "Second", onSelect: vi.fn() },
        ]}
      />,
    );
    await user.click(screen.getByRole("button", { name: "Actions" }));
    await screen.findByRole("menuitem", { name: "First" });
    await user.keyboard("{ArrowDown}");
    await user.keyboard("{Enter}");
    expect(onSelect).toHaveBeenCalledTimes(1);
  });

  it("does not fire onSelect for disabled items", async () => {
    const user = userEvent.setup();
    const onSelect = vi.fn();
    render(
      <DropdownMenu
        trigger={<Button>Actions</Button>}
        items={[{ label: "Blocked", disabled: true, onSelect }]}
      />,
    );
    await user.click(screen.getByRole("button", { name: "Actions" }));
    const item = await screen.findByRole("menuitem", { name: "Blocked" });
    expect(item).toHaveAttribute("aria-disabled", "true");
    await user.click(item);
    expect(onSelect).not.toHaveBeenCalled();
  });

  it("marks destructive items with the destructive token", async () => {
    const user = userEvent.setup();
    render(
      <DropdownMenu
        trigger={<Button>Actions</Button>}
        items={[{ label: "Delete", destructive: true, onSelect: vi.fn() }]}
      />,
    );
    await user.click(screen.getByRole("button", { name: "Actions" }));
    const item = await screen.findByRole("menuitem", { name: "Delete" });
    expect(item.className).toContain("text-destructive");
  });

  it("renders an item icon", async () => {
    const user = userEvent.setup();
    render(
      <DropdownMenu
        trigger={<Button>Actions</Button>}
        items={[
          {
            label: "With icon",
            icon: <svg data-testid="item-icon" />,
            onSelect: vi.fn(),
          },
        ]}
      />,
    );
    await user.click(screen.getByRole("button", { name: "Actions" }));
    await screen.findByRole("menuitem", { name: "With icon" });
    expect(screen.getByTestId("item-icon")).toBeInTheDocument();
  });
});
