import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import {
  Alert,
  AlertDescription,
  AlertTitle,
  Badge,
} from "@/components";

describe("Badge", () => {
  it.each([
    ["primary", "bg-primary", "text-primary-foreground"],
    ["secondary", "bg-secondary", "text-secondary-foreground"],
    ["success", "bg-success", "text-success-foreground"],
    ["warning", "bg-warning", "text-warning-foreground"],
    ["info", "bg-info", "text-info-foreground"],
    ["destructive", "text-destructive", ""],
  ] as const)(
    "renders the %s variant with semantic tokens",
    (variant, bgClass, fgClass) => {
      render(<Badge variant={variant}>Status</Badge>);
      const badge = screen.getByText("Status");
      expect(badge.className).toContain(bgClass);
      if (fgClass) {
        expect(badge.className).toContain(fgClass);
      }
    },
  );
});

describe("Alert", () => {
  it("renders title and description", () => {
    render(
      <Alert>
        <AlertTitle>Heads up</AlertTitle>
        <AlertDescription>Something happened</AlertDescription>
      </Alert>,
    );
    expect(screen.getByText("Heads up")).toBeInTheDocument();
    expect(screen.getByText("Something happened")).toBeInTheDocument();
  });

  it.each([
    ["success", "text-success"],
    ["warning", "text-warning"],
    ["info", "text-info"],
  ] as const)("uses the %s semantic variant", (variant, cls) => {
    render(
      <Alert variant={variant}>
        <AlertTitle>{variant} alert</AlertTitle>
      </Alert>,
    );
    expect(screen.getByRole("alert").className).toContain(cls);
  });
});
