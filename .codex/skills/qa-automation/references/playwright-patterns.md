# Playwright Patterns

## Locator Policy

Prefer resilient, user-facing locators:

```ts
await page.getByRole("button", { name: /submit/i }).click();
await page.getByLabel(/email/i).fill(email);
await page.getByPlaceholder(/search/i).fill("invoice");
await page.getByTestId("order-status").click();
```

Use CSS only for stable structural hooks or existing project conventions. Avoid XPath, generated class names, nth-child chains, and text-only locators for repeated elements unless filtered.

## Assertions

Use web-first assertions that auto-retry:

```ts
await expect(page).toHaveURL(/dashboard/);
await expect(page.getByRole("alert")).toContainText(/success/i);
await expect(page.getByRole("button", { name: /submit/i })).toBeEnabled();
```

Avoid assertion-light scripts. Every test should prove a user-visible result, API result, or persisted state.

## Flow Shape

Use `test.step()` for multi-action business flows:

```ts
test("user can register", async ({ page }) => {
  await test.step("Open registration page", async () => {
    await page.goto("/register");
  });

  await test.step("Submit valid account details", async () => {
    await page.getByLabel(/email/i).fill(`user-${Date.now()}@test.local`);
    await page.getByLabel(/password/i).fill("Test@12345");
    await page.getByRole("button", { name: /register|sign up/i }).click();
  });

  await test.step("Verify registration succeeded", async () => {
    await expect(page.getByRole("alert")).toContainText(/success|created/i);
  });
});
```

## Page Objects And Flows

Prefer page objects for stable page-level behavior and flow objects for cross-page journeys. Keep assertions in specs unless the project already puts domain assertions in flows.

Good page object traits:

- Constructor accepts `Page`.
- Locators are readonly properties or small methods.
- Methods perform user actions, not hidden assertions.
- Public method names use business language: `submitValidRegistration`, `acceptPrivacyPolicy`, `expectValidationMessage`.

## Fixtures

Use fixtures for repeated setup and injection:

```ts
type Fixtures = {
  registerPage: RegisterPage;
};

export const test = base.extend<Fixtures>({
  registerPage: async ({ page }, use) => {
    await use(new RegisterPage(page));
  },
});
```

Prefer fixtures over `new PageObject(page)` in every spec when the repo already uses injected flows.

## Waiting

Use specific waits:

```ts
const responsePromise = page.waitForResponse(
  (res) => res.url().includes("/api/register") && res.request().method() === "POST",
);
await page.getByRole("button", { name: /submit/i }).click();
const response = await responsePromise;
expect(response.status()).toBe(201);
```

Avoid:

```ts
await page.waitForTimeout(2000);
await page.goto(url, { waitUntil: "networkidle" });
```

## Test Data

Generate unique data for create/register flows. Keep secrets in env vars. Clean up created data through API when practical.

```ts
const suffix = Date.now();
const email = `qa-${suffix}@test.local`;
```
