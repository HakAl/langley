import { test, expect } from '@playwright/test';
import AxeBuilder from '@axe-core/playwright';
import { setupMocks, authenticate } from './fixtures';

test.describe('Accessibility (axe-core)', () => {
  test.beforeEach(async ({ page }) => {
    await setupMocks(page);
    await page.goto('/');
    await authenticate(page);
  });

  test('flows view has no critical a11y violations', async ({ page }) => {
    await expect(page.getByText('api.anthropic.com')).toBeVisible();

    const results = await new AxeBuilder({ page })
      .withTags(['wcag2a', 'wcag2aa'])
      .disableRules(['color-contrast']) // theme-dependent, tested visually
      .analyze();

    expect(results.violations.filter(v => v.impact === 'critical')).toEqual([]);
  });

  test('analytics view has no critical a11y violations', async ({ page }) => {
    await page.getByRole('button', { name: 'Analytics' }).click();
    await expect(page.getByText('Total Flows')).toBeVisible();

    const results = await new AxeBuilder({ page })
      .withTags(['wcag2a', 'wcag2aa'])
      .disableRules(['color-contrast'])
      .analyze();

    expect(results.violations.filter(v => v.impact === 'critical')).toEqual([]);
  });

  test('tasks view has no critical a11y violations', async ({ page }) => {
    await page.getByRole('button', { name: 'Tasks' }).click();
    await expect(page.getByRole('columnheader', { name: 'Task ID' })).toBeVisible();

    const results = await new AxeBuilder({ page })
      .withTags(['wcag2a', 'wcag2aa'])
      .disableRules(['color-contrast'])
      .analyze();

    expect(results.violations.filter(v => v.impact === 'critical')).toEqual([]);
  });

  test('tools view has no critical a11y violations', async ({ page }) => {
    await page.getByRole('button', { name: 'Tools' }).click();
    await expect(page.getByRole('columnheader', { name: 'Tool' })).toBeVisible();

    const results = await new AxeBuilder({ page })
      .withTags(['wcag2a', 'wcag2aa'])
      .disableRules(['color-contrast'])
      .analyze();

    expect(results.violations.filter(v => v.impact === 'critical')).toEqual([]);
  });

  test('anomalies view has no critical a11y violations', async ({ page }) => {
    await page.getByRole('button', { name: /Anomalies/ }).click();
    await expect(page.getByText('high cost')).toBeVisible();

    const results = await new AxeBuilder({ page })
      .withTags(['wcag2a', 'wcag2aa'])
      .disableRules(['color-contrast'])
      .analyze();

    expect(results.violations.filter(v => v.impact === 'critical')).toEqual([]);
  });

  test('settings view has no critical a11y violations', async ({ page }) => {
    await page.getByRole('button', { name: 'Settings' }).click();
    await expect(page.getByRole('heading', { name: 'Settings' })).toBeVisible();

    const results = await new AxeBuilder({ page })
      .withTags(['wcag2a', 'wcag2aa'])
      .disableRules(['color-contrast'])
      .analyze();

    expect(results.violations.filter(v => v.impact === 'critical')).toEqual([]);
  });
});
