import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { Button } from "@/components";

describe("Button", () => {
  it("uses the primary variant when none is given", () => {
    render(<Button>Tiếp tục</Button>);
    const button = screen.getByRole("button", { name: "Tiếp tục" });
    expect(button).toHaveAttribute("data-variant", "primary");
    expect(button.className).toContain("bg-primary");
    expect(button.className).toContain("text-primary-foreground");
  });

  it.each([
    ["secondary", "bg-secondary", "text-secondary-foreground"],
    ["success", "bg-success", "text-success-foreground"],
    ["warning", "bg-warning", "text-warning-foreground"],
    ["info", "bg-info", "text-info-foreground"],
    ["outline", "border-border", "bg-background"],
    ["ghost", "hover:bg-muted", ""],
  ] as const)(
    "renders the %s variant with semantic token classes",
    (variant, bgClass, fgClass) => {
      render(<Button variant={variant}>Action</Button>);
      const button = screen.getByRole("button", { name: "Action" });
      expect(button).toHaveAttribute("data-variant", variant);
      expect(button.className).toContain(bgClass);
      if (fgClass) {
        expect(button.className).toContain(fgClass);
      }
    },
  );

  it("renders the destructive variant", () => {
    render(<Button variant="destructive">Delete</Button>);
    const button = screen.getByRole("button", { name: "Delete" });
    expect(button).toHaveAttribute("data-variant", "destructive");
    expect(button.className).toContain("text-destructive");
  });

  it("keeps the link variant available for registry internals", () => {
    render(<Button variant="link">More</Button>);
    const button = screen.getByRole("button", { name: "More" });
    expect(button).toHaveAttribute("data-variant", "link");
  });

  it("blocks clicks and marks busy while loading", async () => {
    const user = userEvent.setup();
    const onClick = vi.fn();
    render(
      <Button loading onClick={onClick}>
        Save
      </Button>,
    );
    const button = screen.getByRole("button", { name: /Save/ });
    expect(button).toBeDisabled();
    expect(button).toHaveAttribute("aria-busy", "true");
    expect(button.querySelector("svg")).not.toBeNull();
    await user.click(button);
    expect(onClick).not.toHaveBeenCalled();
  });

  it("blocks clicks when disabled", async () => {
    const user = userEvent.setup();
    const onClick = vi.fn();
    render(
      <Button disabled onClick={onClick}>
        Save
      </Button>,
    );
    await user.click(screen.getByRole("button", { name: "Save" }));
    expect(onClick).not.toHaveBeenCalled();
  });

  it("renders icon children", () => {
    render(
      <Button>
        <svg data-testid="btn-icon" data-icon="inline-start" />
        With icon
      </Button>,
    );
    expect(screen.getByTestId("btn-icon")).toBeInTheDocument();
  });

  it("merges a custom className", () => {
    render(<Button className="w-full">Wide</Button>);
    expect(screen.getByRole("button", { name: "Wide" }).className).toContain(
      "w-full",
    );
  });

  it("forwards a ref to the underlying button element", () => {
    const ref = { current: null as HTMLButtonElement | null };
    render(<Button ref={ref}>Ref target</Button>);
    expect(ref.current).toBeInstanceOf(HTMLButtonElement);
  });
});
