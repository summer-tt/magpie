// Run with Node's test runner and Playwright on the module path; see README.md.
// A routing alias makes a second, qualified model of each model its key or
// account serves: provider/model@alias. The Routing page's member picker
// offers it to be named into a group explicitly, while the group's patterns
// (matchedBy, patternCount) skip the derived entries: a p/* pattern must
// not sweep every account's variant of a model into the group by itself
// (design §9). The qualified option is picked by its id through the
// picker's own filter, so the test asks nothing of how the entry is
// labelled. In English and Chinese.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const models = [
  { id: "or/b", name: "b", providerName: "OpenRouter", icon: "generic" },
  { id: "or/b@work", name: "b", providerName: "OpenRouter", icon: "generic", routingAlias: "work", baseId: "or/b" },
  { id: "or/c", name: "c", providerName: "OpenRouter", icon: "generic" },
];
const members = ["or/b"];
const groups = () => ({
  models, pools: [],
  groups: [{ id: "free", name: "Free", members, match: [], matched: [], fast: [], off: [], routing: "order", ready: true,
    patterns: [], memberInfo: members.map((id) => ({ id, ready: true })) }],
});

const words = {
  en: { edit: "Edit", save: "Save", add: "Add pattern", two: "2 models" },
  zh: { edit: "编辑", save: "保存", add: "添加模式", two: "2 个模型" },
};

function serve(lang, posts) {
  const state = { agents: [{ id: "codex", name: "Codex", path: "/test/codex", fields: [] }], profiles: [], settings: { lang, theme: "light" } };
  return async (r) => {
    const url = new URL(r.request().url());
    const json = (data) => r.fulfill({ json: data });
    if (url.pathname === "/boot.js") return r.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return r.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) return new Promise(() => {}); // nothing more comes
      return json({ mine: true, now: new Date().toISOString(), seq: 1, totals: { requests: 0, rerouted: 0, errors: 0 }, routes: [] });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/groups") return json(groups());
    if (url.pathname.startsWith("/api/groups/")) {
      posts.push({ path: url.pathname, body: JSON.parse(r.request().postData() || "{}") });
      return json(groups());
    }
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3999" } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await r.fulfill({ body: await fs.readFile(file), contentType });
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    const w = words[lang];
    test(`${engine} ${lang}: a group's patterns skip qualified models, its picker names one`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const page = await (await browser.newContext({ viewport: { width: 1100, height: 2000 }, reducedMotion: "reduce" })).newPage();
      t.after(() => browser.close());
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [], posts = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang, posts));
      await page.goto("http://magpie.test/?view=groups");
      const card = page.locator(".rt-group", { hasText: "Free" });
      await card.waitFor();
      await card.locator("button", { hasText: w.edit }).click();
      const ed = page.locator(".rt-gedit");
      await ed.waitFor();
      const names = () => ed.locator(".fallback:not(.rt-pats) .fbrow .n > span:first-child").allTextContents();

      // the pattern counts base models only: the qualified entry is a
      // derived view of or/b, not another model the pattern found
      const pbox = ed.locator(".rt-pats");
      await pbox.locator("input").fill("or/*");
      await pbox.locator("button", { hasText: w.add }).click();
      assert.equal(await pbox.locator(".fbrow", { hasText: "or/*" }).locator("small").textContent(), w.two,
        "the pattern's count leaves the qualified variant out");
      const rows = await names();
      assert.ok(rows.includes("c"), "the pattern's other base model is in the group");
      assert.ok(!rows.some((n) => n.includes("@work")), `a pattern must not add a qualified variant by itself: ${rows}`);

      // the qualified model can still be named into the group, picked by
      // its whole id through the picker's own filter
      await ed.locator(".rt-madds button.rt-gadd").click();
      await page.locator("#pop:not([hidden]) #q").fill("or/b@work");
      await page.locator("#pop #list li").first().waitFor();
      const offered = await page.locator("#pop #list li").count();
      assert.equal(offered, 1, "filtering the whole qualified id leaves the one entry it names");
      await page.locator("#pop #list li").first().click();
      await page.keyboard.press("Escape");
      const after = await names();
      assert.equal(after.length, 3, `the qualified model joined the group's members by itself: ${after}`);

      const b = ed.locator("button.primary", { hasText: w.save });
      await b.scrollIntoViewIfNeeded();
      await b.click();
      await page.waitForFunction(() => !document.querySelector(".rt-gedit"));
      const sent = posts.find((p) => p.path === "/api/groups/save");
      assert.ok(sent, "the group was saved");
      assert.ok(sent.body.members.includes("or/b@work"), `the qualified member is saved by its whole reference: ${JSON.stringify(sent.body.members)}`);
      assert.deepEqual(sent.body.match, ["or/*"]);
      assert.deepEqual(errors, []);
    });
  }
}
