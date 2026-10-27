// Run with Node's test runner and Playwright on the module path; see README.md.
// The Usage page's card of a Bailian Token Plan 个人版 (bailian_usage.go,
// #1506): the month's share used and the credits the plan counts (the
// share of the quota config's month), when the month resets, and the
// plan's own term — expired, not renewed, since its auto-renew is off.
// The response shape follows the service, with synthetic usage figures:
// 25% of 180,000 credits, resetting when the plan ends. No backend; the
// API is faked here and no account data is included.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const reset = new Date(Date.now() + 6 * 86400000);
reset.setUTCHours(0, 0, 0, 0);
const quotas = [
  {
    provider: "bailian-token-plan", name: "Bailian Token Plan", icon: "bailian-color",
    plan: "Bailian Token Plan · pro", windows: [{ name: "Month", used: 25, limit: 180000, amount: 45000, unit: "credits", resetsAt: reset.toISOString() }],
    until: reset.toISOString(), renew: "off",
  },
];
const provider = {
  id: "bailian-token-plan", name: "Bailian Token Plan", icon: "bailian-color", preset: "bailian-token-plan",
  chat: "https://token-plan.cn-beijing.maas.aliyuncs.com/compatible-mode/v1", catalog: "",
  models: [{ id: "qwen3.8-max", name: "qwen3.8-max", on: true }], agents: [], fallback: [], headers: {},
  key: { set: true, masked: "sk-sp…ab12" }, keyList: [], balanceToken: { takes: false, set: false }, proxy: "",
  accessKey: { vendor: "bailian", id: "LTAIsaved", secretSet: true },
};

function serve(lang) {
  const settings = { theme: "light", lang, tray: "panel", quotaLeft: false, currency: "usd" };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (body) => route.fulfill({ json: body });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings });
    if (url.pathname === "/api/settings") return json(settings);
    if (url.pathname === "/api/providers") return json({ providers: [provider], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/usage/quotas") return json(quotas);
    if (url.pathname === "/api/usage/quotas/history") return json([]);
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const words = {
  en: { used: "25% used", credits: "45,000 / 180,000 credits", month: "Month", expires: "Expires", resets: "Resets" },
  zh: { used: "已用 25%", credits: "45,000 / 180,000 积分", month: "本月", expires: "到期", resets: "重置于" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const [lang, w] of Object.entries(words)) {
    for (const width of [900, 420]) {
      test(`${engine} ${lang} ${width}px: a Bailian Token Plan card shows the month's used share, its credits, the reset and that the plan expires`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(async () => {
          if (process.env.ARTIFACT_DIR) {
            await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
            await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${width}-bailian-plan-card.png`), fullPage: true });
          }
          await browser.close();
        });
        const errors = [];
        const page = await (await browser.newContext({ viewport: { width, height: 800 }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(5000);
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang));
        await page.goto("http://magpie.test/?view=usage");

        const card = page.locator(".subscription-card", { hasText: "Bailian Token Plan" });
        await card.locator(".plan").waitFor();
        const plan = await card.locator(".plan").textContent();
        assert(plan.includes("Bailian Token Plan · pro"), plan);
        assert(plan.includes(w.expires), `the plan's end: ${plan}`);
        assert(!plan.includes(lang === "zh" ? "续费" : "Renews"), "not on auto-renew, so never said to renew");

        const n = card.locator(".quota-n", { hasText: w.credits });
        await n.waitFor();
        assert((await n.textContent()).includes(w.used), await n.textContent());
        assert.equal(await card.locator(".quota-name", { hasText: w.month }).count(), 1, w.month);
        assert.equal(await card.locator(".subscription-error").count(), 0);
        const resetLine = card.locator(".quota-reset");
        assert((await resetLine.textContent()).includes(w.resets), await resetLine.textContent());

        const over = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
        assert(over <= 0, `the page scrolls sideways by ${over}px`);
        assert.deepEqual(errors, []);
      });
    }
  }
}
