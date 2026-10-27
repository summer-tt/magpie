// Run with Node's test runner and Playwright on the module path; see README.md.
// Bailian's Token Plan 个人版 tells its month only through its console
// gateway, traded for the Alibaba Cloud account's AccessKey ID and Secret
// (bailian_usage.go, #1506): a Bailian Token Plan provider's editor asks
// for them, named as Alibaba Cloud names them, with a hint that says what
// they are for. The saved Secret is never shown, only that one is saved;
// Save posts the AccessKey ID and a Secret only when a new one was typed,
// Remove posts clearAccessKey, and an AccessKey ID with no Secret is
// refused. The Qwen AI platform's token plan takes none. A 420px window;
// English and Chinese, every string in every language.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const chat = "https://token-plan.cn-beijing.maas.aliyuncs.com/compatible-mode/v1";
const anthropic = "https://token-plan.cn-beijing.maas.aliyuncs.com/apps/anthropic";
// as /api/providers serves the preset: an access key at every one of its
// endpoints (presetAccessKey)
const bailian = (added) => ({
  id: "bailian-token-plan", name: "Bailian Token Plan", short: "Bailian Plan", icon: "bailian-color", kind: "vendor", added,
  chat, anthropic, decide: chat, models: [{ id: "qwen3.8-max", name: "qwen3.8-max", on: true }],
  accessKey: true, accessKeyVendor: "bailian",
});
const saved = (id, name, at, accessKey) => ({
  id, name, icon: "bailian-color", preset: "bailian-token-plan", ...at, catalog: "",
  models: [{ id: "qwen3.8-max", name: "qwen3.8-max", on: true }], agents: [], fallback: [], headers: {},
  key: { set: true, masked: "sk-sp…ab12" }, keyList: [], balanceToken: { takes: false, set: false }, proxy: "", accessKey,
});
const plan = saved("bailian-token-plan", "Bailian Token Plan", { chat, anthropic, decide: chat }, { vendor: "bailian", id: "LTAIsaved", secretSet: true });
// the Qwen AI platform's token plan is another vendor's: no access key
const qwenPlan = {
  id: "qwen-token-plan", name: "Qwen Token Plan", icon: "qwen-color", preset: "qwen-token-plan", catalog: "",
  chat: "https://token-plan.maas.qianwenaiapi.com/compatible-mode/v1", anthropic: "https://token-plan.maas.qianwenaiapi.com/apps/anthropic",
  models: [{ id: "qwen3.8-max", name: "qwen3.8-max", on: true }], agents: [], fallback: [], headers: {},
  key: { set: true, masked: "sk-sp…cd34" }, keyList: [], balanceToken: { takes: false, set: false }, proxy: "",
};
const relay = {
  id: "relay", name: "Relay", icon: "generic", chat: "https://relay.example.com/v1", responses: "", anthropic: "", catalog: "",
  models: [], agents: [], fallback: [], headers: {}, key: { set: true, masked: "sk-…one" }, keyList: [],
  balanceToken: { takes: false, set: false }, proxy: "",
};

function serve(lang, list, posts) {
  const presets = [bailian(list.some((x) => x.preset === "bailian-token-plan"))];
  const providers = { providers: list, presets, excluded: [], gateway: { running: true, window: true } };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/providers") return json(providers);
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname.startsWith("/api/provider/") && route.request().method() === "POST") {
      posts.push({ action: url.pathname.slice("/api/provider/".length), body: route.request().postDataJSON() });
      return json(providers);
    }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const hint = "The Bailian Token Plan's monthly usage is told only to the Alibaba Cloud account's access key (RAM console → AccessKeys), not to the plan's sk- key. Give its AccessKey ID and Secret here for the Usage page to show the month's used share and credits; it must be the account the plan is subscribed on, as several sk- keys don't mean several plans. It is used for nothing else.";
const strings = ["AccessKey ID", "AccessKey Secret", hint];
const words = {
  en: { saved: "saved · paste a new one to replace it", remove: "Remove", save: "Save", add: "Add", one: "Give the access key's Secret too", hint },
  zh: { saved: "已保存 · 粘贴新的即可替换", remove: "移除", save: "保存", add: "添加", one: "请同时填写访问密钥的 Secret", hint: "Bailian Token Plan 的每月用量只对阿里云账号的 AccessKey 开放（RAM 控制台 → AccessKey 管理），套餐的 sk- Key 查不到。在这里填入它的 AccessKey ID 和 Secret，用量页就会显示当月已用比例和 Credits；它必须是订阅该套餐的账号，多个 sk- Key 不代表多份套餐额度。仅用于查询用量。" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    const w = words[lang];
    const start = async (t, name, list) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(async () => {
        if (process.env.ARTIFACT_DIR) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${name}-bailian-access-key.png`), fullPage: true });
        }
        await browser.close();
      });
      const page = await (await browser.newContext({ viewport: { width: 420, height: 760 }, reducedMotion: "reduce" })).newPage();
      page.setDefaultTimeout(5000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      const posts = [];
      await page.route("**/*", serve(lang, list, posts));
      await page.goto("http://magpie.test/?view=providers");
      return { page, errors, posts };
    };
    const edit = async (page, name) => {
      await page.locator(".row.provider", { hasText: name }).click();
      // drawn: the proxy is under Advanced, folded
      await page.locator(".editor .proxy-mode").waitFor({ state: "attached" });
    };
    const ak = (page) => page.locator(".editor .volc-ak");
    const sk = (page) => page.locator(".editor .volc-sk");
    const press = async (page, posts, label) => {
      const n = posts.length;
      const b = page.locator(".editor .bar").getByRole("button", { name: label, exact: true });
      await b.scrollIntoViewIfNeeded();
      await b.click();
      for (let i = 0; i < 50 && posts.length === n; i++) await page.waitForTimeout(50);
      return posts.length > n ? posts.at(-1) : null;
    };
    const fits = async (page) => {
      const over = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
      assert(over <= 0, `the page scrolls sideways by ${over}px`);
    };

    test(`${engine} ${lang}: a Bailian Token Plan provider asks for its AccessKey ID and Secret, kept, replaced and removed`, async (t) => {
      const { page, errors, posts } = await start(t, "bailian", [plan, qwenPlan]);
      await edit(page, "Bailian Token Plan");
      const labels = await page.locator(".editor label").allTextContents();
      assert(labels.includes("AccessKey ID") && labels.includes("AccessKey Secret"), labels.join(" | "));
      assert(!labels.includes("Secret Access Key"), "named as Alibaba Cloud names them");
      assert.equal(await ak(page).inputValue(), "LTAIsaved");
      assert.equal(await sk(page).inputValue(), "", "the saved Secret is not shown");
      assert.equal(await sk(page).getAttribute("type"), "password");
      assert.equal(await sk(page).getAttribute("placeholder"), w.saved);
      const tip = await page.locator(".editor .hint").allTextContents();
      assert(tip.some((x) => x.includes(w.hint)), tip.join(" | "));
      assert(!tip.some((x) => x.includes("Volcengine") || x.includes("TokenHub")), "not another vendor's hint");
      await fits(page);

      // left as it is: the AccessKey ID posted, no Secret, nothing cleared
      let body = (await press(page, posts, w.save)).body;
      assert.equal(body.accessKeyID, "LTAIsaved");
      assert(!("secretAccessKey" in body) && !("clearAccessKey" in body), JSON.stringify(body));

      // a new Secret, trimmed
      await edit(page, "Bailian Token Plan");
      await sk(page).fill(" new-secret ");
      body = (await press(page, posts, w.save)).body;
      assert.equal(body.secretAccessKey, "new-secret");

      // Remove clears both; the placeholder names the Secret again
      await edit(page, "Bailian Token Plan");
      await page.locator(".editor").getByRole("button", { name: w.remove, exact: true }).first().click();
      assert.equal(await ak(page).inputValue(), "");
      assert.equal(await sk(page).getAttribute("placeholder"), "AccessKey Secret");
      body = (await press(page, posts, w.save)).body;
      assert.equal(body.accessKeyID, "");
      assert.equal(body.clearAccessKey, true);

      const missing = await page.evaluate((keys) => Object.fromEntries(["zh", "zh-TW", "ja", "de"].map((l) => [l, keys.filter((k) => !I18N[l][k])])), strings);
      assert.deepEqual(missing, { zh: [], "zh-TW": [], ja: [], de: [] }, "every string in every language");
      const border = await page.evaluate(() => [...document.querySelectorAll(".editor, .editor *")].map((e) => getComputedStyle(e).borderLeftWidth).filter((b) => parseFloat(b) > 1));
      assert.deepEqual(border, [], "no border stripes");
      assert.deepEqual(errors, []);
    });

    test(`${engine} ${lang}: the Qwen AI platform's token plan asks for none`, async (t) => {
      const { page, errors, posts } = await start(t, "qwen", [plan, qwenPlan]);
      await edit(page, "Qwen Token Plan");
      assert.equal(await ak(page).count(), 0);
      assert.equal(await sk(page).count(), 0);
      const body = (await press(page, posts, w.save)).body;
      assert.equal(body.id, "qwen-token-plan");
      assert(!("accessKeyID" in body) && !("secretAccessKey" in body), JSON.stringify(body));
      await fits(page);
      assert.deepEqual(errors, []);
    });

    test(`${engine} ${lang}: adding Bailian Token Plan asks for them, and an AccessKey ID alone is refused`, async (t) => {
      const { page, errors, posts } = await start(t, "add", [relay]);
      await page.locator("#addProvider").click();
      await page.locator('#addSheet .tile[data-pick="Bailian Plan"]').click();
      await page.locator(".editor.new").waitFor();
      const labels = await page.locator(".editor label").allTextContents();
      assert(labels.includes("AccessKey ID") && labels.includes("AccessKey Secret"), labels.join(" | "));
      await page.locator(".editor.new input[type=password]").first().fill("sk-sp-key");
      await ak(page).fill(" LTAInew ");
      assert.equal(await press(page, posts, w.add), null, "an AccessKey ID with no Secret posts nothing");
      assert.equal(await page.locator(".editor .editor-error").textContent(), w.one);
      await sk(page).fill("sk-new");
      const body = (await press(page, posts, w.add)).body;
      assert.equal(body.new, true);
      assert.equal(body.preset, "bailian-token-plan");
      assert.equal(body.accessKeyID, "LTAInew");
      assert.equal(body.secretAccessKey, "sk-new");
      await fits(page);
      assert.deepEqual(errors, []);
    });
  }
}
