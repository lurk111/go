import fs from "node:fs/promises";
import path from "node:path";
import { createRequire } from "node:module";
import { fileURLToPath } from "node:url";
const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const require = createRequire(path.join(root, "web/package.json"));
const { chromium } = require("playwright");
const config = await fs.readFile(path.join(root, "deploy/.env"), "utf8");
const token = config
  .split(/\r?\n/)
  .find((s) => s.startsWith("MON_ADMIN_TOKEN="))
  .split("=")[1];
const executable =
  process.env.BROWSER_EXE ||
  "C:/Program Files (x86)/Microsoft/Edge/Application/msedge.exe";
const checks = [];
let browser;
try {
  browser = await chromium.launch({
    executablePath: executable,
    headless: true,
  });
  const context = await browser.newContext({
    viewport: { width: 1440, height: 1000 },
  });
  const page = await context.newPage();
  const errors = [];
  page.on("pageerror", (e) => errors.push(e.message));
  await page.goto("http://127.0.0.1:18080");
  await page.getByLabel("管理凭据").fill(token);
  await page.getByRole("button", { name: "登录观察台 →" }).click();
  await page.getByRole("heading", { name: "异常事件", exact: true }).waitFor();
  checks.push("login_and_dashboard");
  const response = await context.request.post(
    "http://127.0.0.1:18081/scenarios",
    {
      headers: { Authorization: "Bearer " + token },
      data: { scenario: "model_quota" },
    },
  );
  if (!response.ok()) throw new Error("simulator unavailable");
  const task = (await response.json()).task_ids[0];
  await page.getByPlaceholder("输入任务 ID 筛选").fill(task);
  await page.locator(".event-row").first().waitFor({ timeout: 90000 });
  await page.locator(".event-row").first().click();
  await page
    .getByRole("heading", { name: "模型供应商明确返回额度不足", exact: true })
    .waitFor({ timeout: 90000 });
  checks.push("automatic_discovery_and_evidence_report");
  const runtime = await context.request.get("http://127.0.0.1:18080/internal/metrics");
  const modelEnabled = (await runtime.json()).model_enabled;
  if (modelEnabled) {
    const found = await context.request.get("http://127.0.0.1:18080/internal/events?task_id=" + encodeURIComponent(task));
    const event = (await found.json())[0];
    const saved = await context.request.get(`http://127.0.0.1:18080/internal/events/${event.event_id}/report`);
    const report = (await saved.json()).report;
    if (report.diagnosis_mode === "model_assisted") {
      await page.getByText("规则 + AI 分析", { exact: true }).waitFor();
      await page.locator(".report").getByText(/AI 建议（人工核对）/).first().waitFor();
    } else {
      await page.locator(".report .notice").filter({ hasText: "已保留规则诊断" }).waitFor();
    }
    checks.push("model_result_or_explicit_fallback_visible");
  }
  const evidence = page
    .locator("details.evidence")
    .filter({ hasText: "模型调用错误" });
  await evidence.locator("summary").click();
  if (
    !(await evidence
      .locator("pre")
      .innerText()
      .then((x) => x.includes("insufficient_quota")))
  )
    throw new Error("missing evidence");
  checks.push("inspect_model_error_evidence");
  await fs.mkdir(path.join(root, "artifacts"), { recursive: true });
  await page.evaluate(() => window.scrollTo(0, 0));
  await page.screenshot({
    path: path.join(root, "artifacts/go-dashboard-desktop.png"),
    fullPage: true,
  });
  await page
    .getByLabel("关闭原因", { exact: true })
    .fill("页面验收：这是实验故意制造的失败，不需要继续处理");
  await page.getByRole("button", { name: "记录原因并关闭事件" }).click();
  await page
    .locator(".detail .badge.closed_unresolved")
    .first()
    .waitFor({ timeout: 15000 });
  await page
    .getByText("页面验收：这是实验故意制造的失败，不需要继续处理", {
      exact: true,
    })
    .first()
    .waitFor();
  checks.push("manual_close_with_reason");
  await page.setViewportSize({ width: 390, height: 844 });
  if (
    await page.evaluate(
      () => document.documentElement.scrollWidth > innerWidth + 1,
    )
  )
    throw new Error("mobile horizontal overflow");
  await page.screenshot({
    path: path.join(root, "artifacts/go-dashboard-mobile.png"),
    fullPage: true,
  });
  checks.push("mobile_layout");
  await page.setViewportSize({ width: 1440, height: 1000 });
  if (modelEnabled) {
    const soak = JSON.parse(await fs.readFile(path.join(root, "artifacts/go-soak-acceptance.json"), "utf8"));
    const sample = soak.samples.find((s) => s.model_status === "completed");
    if (!sample) throw new Error("real model fixture unavailable; run soak acceptance first");
    // Closing an event intentionally switches the page to the closed filter.
    // Restore all statuses before looking up an earlier, still-open AI report.
    await page.locator(".filters select").first().selectOption("");
    await page.getByPlaceholder("输入任务 ID 筛选").fill(sample.task_id);
    await page.locator(".event-row").filter({ hasText: sample.task_id }).click();
    await page.getByText("规则 + AI 分析", { exact: true }).waitFor();
    await page.getByText("分析中的证据编号对照", { exact: true }).click();
    await page.locator(".report").getByText(/AI 证据分析/).waitFor();
    await page.evaluate(() => window.scrollTo(0, 0));
    await page.screenshot({ path: path.join(root, "artifacts/go-model-report-desktop.png"), fullPage: true });
    await page.setViewportSize({ width: 390, height: 844 });
    if (await page.evaluate(() => document.documentElement.scrollWidth > innerWidth + 1)) throw new Error("model report mobile overflow");
    await page.screenshot({ path: path.join(root, "artifacts/go-model-report-mobile.png"), fullPage: true });
    await page.setViewportSize({ width: 1440, height: 1000 });
    checks.push("real_model_evidence_mapping_and_mobile_layout");
  }
  await page.getByRole("button", { name: /通知记录/ }).click();
  await page.getByRole("heading", { name: "本地接收记录" }).waitFor();
  await page.locator("tbody tr").first().waitFor();
  checks.push("local_notifications_page");
  await page.getByRole("button", { name: /运行状态/ }).click();
  await page.getByRole("heading", { name: "业务数据采集" }).waitFor();
  checks.push("runtime_status_page");
  await page.getByRole("button", { name: /退出登录/ }).click();
  await page.getByLabel("管理凭据").waitFor();
  checks.push("logout");
  if (errors.length) throw new Error("browser page error: " + errors.join(";"));
  checks.push("no_browser_runtime_errors");
  await fs.writeFile(
    path.join(root, "artifacts/go-browser.json"),
    JSON.stringify({ at: new Date().toISOString(), checks, task }, null, 2),
  );
  console.log(
    `PASS ${checks.length} browser checks; screenshots saved in artifacts`,
  );
} catch (error) {
  console.error(String(error.message).replaceAll(token, "[REDACTED]"));
  process.exitCode = 1;
} finally {
  await browser?.close();
}
