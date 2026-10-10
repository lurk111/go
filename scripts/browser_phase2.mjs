import fs from "node:fs/promises";
import path from "node:path";
import { createRequire } from "node:module";
import { fileURLToPath } from "node:url";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const require = createRequire(path.join(root, "web/package.json"));
const { chromium } = require("playwright");
const base = process.env.PHASE2_BROWSER_URL || "http://127.0.0.1:18090";
const installation = process.env.PHASE2_INSTALLATION || path.join(root, "tmp/deploy-business-v2");
const { admin_token: token } = JSON.parse(await fs.readFile(path.join(installation, "deploy/phase2/config/credentials.json"), "utf8"));
const executablePath = process.env.BROWSER_EXE || "C:/Program Files (x86)/Microsoft/Edge/Application/msedge.exe";
const checks = [];
const errors = [];
let browser, context, original;
const assert = (condition, message) => { if (!condition) throw new Error(message); };

async function current() {
  const response = await context.request.get(base + "/internal/config");
  assert(response.ok(), "configuration endpoint unavailable");
  return response.json();
}
async function saveThroughUI(page, document) {
  await page.getByLabel("监测配置编辑器", { exact: true }).fill(JSON.stringify(document, null, 2));
  await page.getByRole("button", { name: "保存并启用配置", exact: true }).click();
  await page.getByRole("status").filter({ hasText: "配置已启用：" }).waitFor();
}
try {
  browser = await chromium.launch({ executablePath, headless: true });
  context = await browser.newContext({ viewport: { width: 1440, height: 1000 } });
  const page = await context.newPage();
  page.on("pageerror", (error) => errors.push(error.message));
  await page.goto(base);
  await page.getByLabel("管理凭据", { exact: true }).fill(token);
  await page.getByRole("button", { name: "登录观察台 →", exact: true }).click();
  await page.getByRole("heading", { name: "异常事件", exact: true }).waitFor();
  checks.push("login_and_dashboard");
  original = (await current()).document;
  assert((original.sources || []).length === 0 && (original.checks || []).length === 0, "use a clean independent business installation for this test");
  const beforeEvents = await (await context.request.get(base + "/internal/events?limit=500&status=")).json();

  await page.getByRole("button", { name: /监测配置/ }).click();
  await page.getByRole("heading", { name: "业务接入与监测定义", exact: true }).waitFor();
  const editor = page.getByLabel("监测配置编辑器", { exact: true });
  await editor.waitFor();
  await page.waitForFunction(() => Boolean(document.querySelector(".config-editor")?.value));
  assert((JSON.parse(await editor.inputValue()).checks || []).length === 0, "empty configuration was not loaded");
  await page.getByRole("heading", { name: "添加数据库连接", exact: true }).waitFor();
  checks.push("empty_configuration_and_connection_form");

  await page.getByRole("button", { name: "校验配置", exact: true }).click();
  await page.getByRole("status").filter({ hasText: "配置校验通过，尚未启用。" }).waitFor();
  await saveThroughUI(page, original);
  assert((await current()).document.checks.length === 0, "empty configuration save failed");
  checks.push("validate_and_save_empty_configuration");

  const validVersion = (await current()).version;
  await editor.fill(JSON.stringify({ ...original, unknown_field_for_validation: true }));
  await page.getByRole("button", { name: "校验配置", exact: true }).click();
  await page.getByRole("status").filter({ hasText: /unknown_field_for_validation|not found/ }).waitFor();
  await page.getByRole("button", { name: "保存并启用配置", exact: true }).click();
  await page.getByRole("status").filter({ hasText: /unknown_field_for_validation|not found/ }).waitFor();
  assert((await current()).version === validVersion, "invalid document changed active configuration");
  checks.push("invalid_configuration_visible_and_not_activated");

  // A missing environment reference guarantees no database connection or secret is used.
  const mock = structuredClone(original);
  mock.sources = [{ id: "ui_mock_source", driver: "mysql", dsn: { env: "BID_MON_UI_ONLY_UNSET_MOCK_DSN" }, budget: { timeout: "1s", max_rows: 10, max_bytes: 4096 } }];
  mock.checks = [{
    id: "ui_toggle_sample", source: "ui_mock_source", mode: "entity", environment: "browser", enabled: false,
    interval: "15s", query: "SELECT id, status FROM mock_jobs", identity: ["id"],
    fields: { id: { column: "id", type: "string", required: true }, status: { column: "status", type: "string", required: true } },
    rules: [{ id: "failed", version: "v1", trigger: "fields.status == 'failed'", recover: "fields.status == 'completed'", confirmations: 2, recovery_confirmations: 2, severity: "warning" }],
    diagnosis_mode: "rule_only", allow_model: false, notification_routes: [],
  }];
  await saveThroughUI(page, mock);
  assert((await current()).document.checks[0].enabled === false, "disabled check was not persisted");
  await page.getByRole("button", { name: "启用检查", exact: true }).click();
  assert(JSON.parse(await editor.inputValue()).checks[0].enabled === true, "enable selection did not update editor");
  await page.getByRole("button", { name: "保存并启用配置", exact: true }).click();
  await page.getByRole("status").filter({ hasText: "配置已启用：" }).waitFor();
  assert((await current()).document.checks[0].enabled === true, "enabled check was not persisted");
  await page.getByRole("button", { name: "暂停检查", exact: true }).click();
  assert(JSON.parse(await editor.inputValue()).checks[0].enabled === false, "pause selection did not update editor");
  await page.getByRole("button", { name: "保存并启用配置", exact: true }).click();
  await page.getByRole("status").filter({ hasText: "配置已启用：" }).waitFor();
  assert((await current()).document.checks[0].enabled === false, "paused check was not persisted");
  checks.push("check_enable_pause_through_editor_and_persistence");

  await saveThroughUI(page, original);
  assert((await current()).document.checks.length === 0, "original empty checks not restored");
  const ready = await context.request.get(base + "/health/ready");
  assert(ready.status() === 200, "empty configuration is not ready after save");
  checks.push("empty_configuration_restored_and_ready");

  await page.getByRole("button", { name: "查看 30 天保留预览", exact: true }).click();
  const result = page.getByRole("status");
  await result.filter({ hasText: "30 天保留策略预览，不删除数据：" }).waitFor();
  const previewText = await result.innerText();
  const preview = JSON.parse(previewText.slice(previewText.indexOf("{")));
  assert(preview.preview_only === true && preview.automatic_cleanup === false, "retention is not explicitly preview only");
  assert(["closed_event_evidence", "inactive_snapshots", "confirmed_deliveries"].every(key => typeof preview[key] === "number"), "retention counts missing");
  const afterEvents = await (await context.request.get(base + "/internal/events?limit=500&status=")).json();
  assert(beforeEvents.length === afterEvents.length, "browser configuration/preview changed event count");
  checks.push("retention_preview_only_and_no_event_changes");
  await page.getByText("自动清理已关闭", { exact: true }).waitFor();

  await fs.mkdir(path.join(root, "artifacts"), { recursive: true });
  await page.evaluate(() => window.scrollTo(0, 0));
  await page.screenshot({ path: path.join(root, "artifacts/phase2-config.png"), fullPage: true });
  await page.setViewportSize({ width: 390, height: 844 });
  const overflow = await page.evaluate(() => ({ viewport: innerWidth, content: document.documentElement.scrollWidth }));
  assert(overflow.content <= overflow.viewport + 1, `mobile horizontal overflow: ${overflow.content}/${overflow.viewport}`);
  await page.screenshot({ path: path.join(root, "artifacts/phase2-config-mobile.png"), fullPage: true });
  checks.push("mobile_configuration_layout");
  await page.setViewportSize({ width: 1440, height: 1000 });
  await page.getByRole("button", { name: /运行状态/ }).click();
  await page.getByText("尚无检查，前往监测配置添加业务。", { exact: true }).waitFor();
  checks.push("runtime_empty_checks_message");
  await page.getByRole("button", { name: /退出登录/ }).click();
  await page.getByLabel("管理凭据", { exact: true }).waitFor();
  checks.push("logout");
  assert(errors.length === 0, "browser runtime errors: " + errors.join("; "));
  checks.push("no_browser_runtime_errors");
  await fs.writeFile(path.join(root, "artifacts/phase2-browser.json"), JSON.stringify({ at: new Date().toISOString(), passed: true, base, checks, synthetic_source_only: true, original_configuration_restored: true, javascript_errors: errors }, null, 2));
  console.log(`PASS ${checks.length} phase 2 browser checks; desktop/mobile screenshots saved`);
} catch (error) {
  console.error(String(error.message).replaceAll(token, "[REDACTED]"));
  await fs.mkdir(path.join(root, "artifacts"), { recursive: true });
  await fs.writeFile(path.join(root, "artifacts/phase2-browser.json"), JSON.stringify({ at: new Date().toISOString(), passed: false, base, checks, error: String(error.message).replaceAll(token, "[REDACTED]") }, null, 2));
  process.exitCode = 1;
} finally {
  if (context && original) {
    // API restoration also covers failures while a mock definition was active.
    await context.request.put(base + "/internal/config", { headers: { Origin: base, "X-Bid-Mon": "1", "Content-Type": "application/json", Authorization: "Bearer " + token }, data: original }).catch(() => {});
  }
  await browser?.close();
}
