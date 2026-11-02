const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const assets = path.resolve(__dirname, "../assets");

function fixture(kind, count) {
  const p = { id: kind === "account" ? "codex" : "alias-relay", name: kind === "account" ? "Codex" : "Alias relay",
    chat: kind === "key" ? "http://127.0.0.1:1/v1" : "", key: kind === "key" ? "synthetic-key" : "", responses: "", anthropic: "", catalog: "", models: [{ id: "m", on: true }],
    agents: [], fallback: [], headers: {}, keyList: [], accountNames: { "one@example.com": "Display name" } };
  if (kind === "account") p.account = { id: "login-one", agent: "codex", agentName: "Codex", user: "one@example.com", plan: "Plus", routingAlias: "",
    logins: Array.from({ length: count }, (_, i) => ({ id: i ? "login-two" : "login-one", user: i ? "two@example.com" : "one@example.com", plan: "Plus", active: !i, on: true, routingAlias: "" })) };
  else p.keyList = Array.from({ length: count }, (_, i) => ({ id: i ? "key-two" : "key-one", name: i ? "Second key" : "First key", masked: i ? "sk-…2222" : "sk-…1111", on: true, active: !i, routingAlias: "" }));
  return { providers: [p], presets: [], excluded: [], gateway: { running: true, window: true } };
}
function serve(lang, providers, posts) {
  const settings = { theme: "light", lang, tray: "panel", currency: "usd" };
  return async (route) => {
    const u = new URL(route.request().url()), json = (data) => route.fulfill({ json: data });
    if (u.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs={lang:${JSON.stringify(lang)},theme:"light",web:true};` });
    if (u.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window={};" });
    if (u.pathname === "/api/state") return json({ agents: [], profiles: [], settings });
    if (u.pathname === "/api/settings") return json(settings);
    if (u.pathname === "/api/providers") return json(providers);
    if (u.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (u.pathname === "/api/plugins") return json({ plugins: [] });
    if (u.pathname === "/api/provider/accountalias" || u.pathname === "/api/keys/alias") {
      const body = route.request().postDataJSON(); posts.push({ path: u.pathname, body });
      const p = providers.providers[0];
      if (u.pathname.endsWith("accountalias")) {
        const l = p.account.logins.find((l) => l.id === body.account);
        assert.ok(l, "POST identifies stable login ID, never email or display name");
        l.routingAlias = body.alias.trim().toLowerCase();
        if (l.active) p.account.routingAlias = l.routingAlias;
      } else p.keyList.find((k) => k.id === body.ref).routingAlias = body.alias.trim().toLowerCase();
      return json(providers);
    }
    if (u.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, u.pathname === "/" ? "index.html" : u.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

for (const engine of process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"]) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    for (const kind of ["account", "key"]) for (const count of [1, 2]) {
      test(`${engine} ${lang}: routing alias on ${count} ${kind} rows`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        for (const width of [1100, 360]) {
          const context = await browser.newContext({ viewport: { width, height: 800 }, reducedMotion: "reduce" });
          const page = await context.newPage(), posts = [], errors = [], data = fixture(kind, count);
          page.on("pageerror", (e) => errors.push(e.message));
          await page.route("**/*", serve(lang, data, posts));
          page.setDefaultTimeout(5000);
          page.setDefaultNavigationTimeout(60000);
          await page.goto("http://magpie.test/?view=providers");
          await page.locator(".row.provider", { hasText: data.providers[0].name }).first().click();
          await page.locator(".editor .accts .acc").first().waitFor();
          const label = await page.evaluate(() => t("Routing alias"));
          const entries = page.locator(".editor").getByRole("button", { name: label, exact: true }).or(page.locator(".editor").getByRole("textbox", { name: label, exact: true }));
          assert.equal(await entries.count(), count, "every account/key, including the first and a single account, has a separate Routing alias editor");
          for (let i = 0; i < count; i++) {
            // Existing account rows may retain their display identity; route owner comes from JSON id.
            const row = page.locator(".editor .accts .acc").nth(i);
            const entry = row.getByRole("button", { name: label, exact: true }).or(row.getByRole("textbox", { name: label, exact: true }));
            assert.equal(await entry.count(), 1, "one accessible routing editor in the matching row");
            const open = async () => {
              const e = row.getByRole("button", { name: label, exact: true });
              if (await e.count()) await e.click();
              return row.locator("input:not([type=checkbox]):not([type=password])").last();
            };
            let input = await open();
            await input.fill("cancelled"); await input.press("Escape");
            assert.equal(posts.length, i, "Escape writes nothing");
            input = await open(); await input.fill(i ? "backup" : "work");
            await input.press("Enter");
            await page.waitForFunction((wanted) => [...document.querySelectorAll(".editor .acc")].some((r) => r.textContent.includes(wanted) || [...r.querySelectorAll("input")].some((e) => e.value === wanted)), i ? "backup" : "work");
            assert.equal(posts.length, i + 1, "Enter saves once, including subsequent blur");
            assert.deepEqual(posts[i], { path: kind === "account" ? "/api/provider/accountalias" : "/api/keys/alias", body: kind === "account" ?
              { id: "codex", account: i ? "login-two" : "login-one", alias: i ? "backup" : "work" } :
              { id: "alias-relay", ref: i ? "key-two" : "key-one", alias: i ? "backup" : "work" } });
          }
          assert.deepEqual(data.providers[0].accountNames, { "one@example.com": "Display name" }, "routing alias does not rename the account");
          assert.equal(await page.locator("select").count(), 0);
          assert.equal(await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth), 0, `${width}px must fit`);
          assert.deepEqual(errors, []);
          await context.close();
        }
      });
    }
  }
}

// The alias editor's whole life on one row, and the display name beside it
// staying its own (design §5): an alias is set, renamed, and cleared by
// emptying the same input; a name another row already has comes back as the
// service's own error with the typed value kept to be corrected; and
// renaming the display name writes nothing to the alias and loses nothing
// of it. The alias endpoints take the same bodies as above; the display
// name goes on posting to accountname / keys rename as it always did.
function serveEdit(lang, providers, posts) {
  const inner = serve(lang, providers, posts);
  return async (route) => {
    const u = new URL(route.request().url());
    const aliasPath = u.pathname === "/api/provider/accountalias" || u.pathname === "/api/keys/alias";
    const renamePath = u.pathname === "/api/provider/accountname" || u.pathname === "/api/keys/rename";
    if (!aliasPath && !renamePath) return inner(route);
    const body = route.request().postDataJSON(), p = providers.providers[0];
    posts.push({ path: u.pathname, body });
    if (aliasPath) {
      const alias = body.alias.trim().toLowerCase();
      const rows = u.pathname.endsWith("accountalias") ? p.account.logins : p.keyList;
      const mine = rows.find((r) => r.id === (u.pathname.endsWith("accountalias") ? body.account : body.ref));
      if (alias && rows.some((r) => r !== mine && r.routingAlias === alias)) {
        return route.fulfill({ status: 409, json: { error: `routing alias "${alias}" belongs to another ${u.pathname.endsWith("accountalias") ? "account" : "key"}` } });
      }
      mine.routingAlias = alias;
      return route.fulfill({ json: providers });
    }
    if (u.pathname.endsWith("accountname")) {
      if (body.alias) p.accountNames[body.account] = body.alias; else delete p.accountNames[body.account];
      return route.fulfill({ json: providers });
    }
    p.keyList.find((k) => k.id === body.ref).name = body.name;
    return route.fulfill({ json: providers });
  };
}

for (const engine of process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"]) {
  for (const lang of ["en", "zh"]) {
    for (const kind of ["account", "key"]) {
      test(`${engine} ${lang}: routing alias changed and cleared on a ${kind}, a taken name corrected, the display name its own`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        for (const width of [1100, 360]) {
          const context = await browser.newContext({ viewport: { width, height: 800 }, reducedMotion: "reduce" });
          const page = await context.newPage(), posts = [], errors = [], data = fixture(kind, 2);
          page.on("pageerror", (e) => errors.push(e.message));
          await page.route("**/*", serveEdit(lang, data, posts));
          page.setDefaultTimeout(5000);
          page.setDefaultNavigationTimeout(60000);
          await page.goto("http://magpie.test/?view=providers");
          await page.locator(".row.provider", { hasText: data.providers[0].name }).first().click();
          await page.locator(".editor .accts .acc").first().waitFor();
          const label = await page.evaluate(() => t("Routing alias"));
          const rows = page.locator(".editor .accts .acc");
          const aliasButton = (i) => rows.nth(i).getByRole("button", { name: label, exact: true });
          const says = (want) => page.waitForFunction(([i, want]) => {
            const r = document.querySelectorAll(".editor .accts .acc")[i];
            return r && [...r.querySelectorAll("button")].some((b) => b.textContent === want);
          }, [0, want]).catch(() => { throw new Error(`row never said ${want}`); });
          const openAlias = async (i) => {
            const b = aliasButton(i);
            await b.click();
            return rows.nth(i).locator("input:not([type=checkbox]):not([type=password])").last();
          };

          // set, from empty
          let input = await openAlias(0);
          await input.fill("work");
          await input.press("Enter");
          await says("@work");
          // the display name is its own editor: renaming it posts nothing
          // to the alias and leaves the alias saying itself
          await rows.nth(0).locator(".n.rename").click();
          const nameIn = rows.nth(0).locator("input.rename-in");
          await nameIn.fill("Desk");
          await nameIn.press("Enter");
          await page.waitForFunction(() => document.querySelectorAll(".editor .accts .acc")[0]?.textContent.includes("Desk"));
          assert.equal(posts.length, 2, "renaming the display name makes no alias post");
          await says("@work");
          // change, starting from the saved alias
          input = await openAlias(0);
          assert.equal(await input.inputValue(), "work", "the editor opens with the saved alias");
          await input.fill("team");
          await input.press("Enter");
          await says("@team");
          // a name another row already has: the service's error is said in
          // the editor, the typed value stays to be corrected
          input = await openAlias(1);
          await input.fill("team");
          await input.press("Enter");
          await page.locator(".editor .editor-error").waitFor();
          assert.equal(await page.locator(".editor .editor-error").textContent(),
            kind === "account" ? 'routing alias "team" belongs to another account' : 'routing alias "team" belongs to another key');
          assert.equal(await input.inputValue(), "team", "the refused value is kept for correcting");
          await input.fill("backup");
          await input.press("Enter");
          await page.waitForFunction(() => {
            const r = document.querySelectorAll(".editor .accts .acc")[1];
            return r && [...r.querySelectorAll("button")].some((b) => b.textContent === "@backup");
          });
          assert.equal((await page.locator(".editor .editor-error").count()) === 0, true, "a later save clears the error");
          // clearing: the same input emptied, the row back to no alias
          input = await openAlias(1);
          assert.equal(await input.inputValue(), "backup", "the editor opens with the saved alias");
          await input.fill("");
          await input.press("Enter");
          await page.waitForFunction((label) => {
            const r = document.querySelectorAll(".editor .accts .acc")[1];
            return r && [...r.querySelectorAll("button")].some((b) => b.textContent === label && b.getAttribute("aria-label") === label);
          }, label);
          assert.equal(posts.length, 6, "six posts: set, rename, change, refused, corrected, cleared");
          assert.deepEqual(posts.map((p) => [p.path, p.body.alias ?? p.body.name]), kind === "account" ? [
            ["/api/provider/accountalias", "work"],
            ["/api/provider/accountname", "Desk"],
            ["/api/provider/accountalias", "team"],
            ["/api/provider/accountalias", "team"],
            ["/api/provider/accountalias", "backup"],
            ["/api/provider/accountalias", ""],
          ] : [
            ["/api/keys/alias", "work"],
            ["/api/keys/rename", "Desk"],
            ["/api/keys/alias", "team"],
            ["/api/keys/alias", "team"],
            ["/api/keys/alias", "backup"],
            ["/api/keys/alias", ""],
          ]);
          assert.deepEqual(posts.map((p) => p.body.id), Array(6).fill(kind === "account" ? "codex" : "alias-relay"), "every post names the provider");
          assert.deepEqual(data.providers[0].accountNames,
            kind === "account" ? { "one@example.com": "Desk" } : { "one@example.com": "Display name" },
            "the alias posts never touch the display names");
          assert.equal(await page.locator("select").count(), 0);
          assert.equal(await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth), 0, `${width}px must fit`);
          assert.deepEqual(errors, []);
          await context.close();
        }
      });
    }
  }
}

// The qualified reference copy entry (design §5): a row whose account or
// key has an active alias offers, under the frozen accessible name
// t("Copy model reference"), to put the whole provider/model@alias on the
// clipboard for a model that account serves — here the one model the
// provider lists, copied as itself, with no effort or window appended. It
// is reached and used from the keyboard, fits 360px, and writes nothing:
// no POST leaves the page. A row without an alias is not required to have
// one. The label, the entry point's shape and any menu of several models
// are the production author's; what is pinned is the name, the row, the
// copied reference itself and that it is the user's keyboard to use.
function serveWrites(lang, providers, writes) {
  const inner = serve(lang, providers, []);
  return async (route) => {
    const u = new URL(route.request().url());
    if (u.pathname.startsWith("/api/") && route.request().method() === "POST") writes.push(u.pathname);
    return inner(route);
  };
}
for (const engine of process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"]) {
  for (const lang of ["en", "zh"]) {
    for (const kind of ["account", "key"]) {
      test(`${engine} ${lang}: copying the qualified reference of an aliased ${kind}`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        for (const width of [1100, 360]) {
          // WebKit's clipboard permission table takes only clipboard-read
          // (clipboard-write is a Chromium permission; granting it there
          // fails the context). The copy is the page's own real write — the
          // keyboard's activation — and readText reads back whichever real
          // way it landed, the API or the copy command.
          const context = await browser.newContext({ viewport: { width, height: 800 }, reducedMotion: "reduce",
            permissions: engine === "webkit" ? ["clipboard-read"] : ["clipboard-read", "clipboard-write"] });
          const page = await context.newPage(), writes = [], errors = [], data = fixture(kind, 1);
          const row0 = kind === "account" ? data.providers[0].account : data.providers[0].keyList[0];
          row0.routingAlias = "work";
          if (kind === "account") row0.logins[0].routingAlias = "work";
          page.on("pageerror", (e) => errors.push(e.message));
          await page.route("**/*", serveWrites(lang, data, writes));
          // the clipboard API only exists in a secure context: the page is
          // served over https, still fulfilled from the same routes with no
          // network and the same paths and APIs as every other test here
          page.setDefaultTimeout(5000);
          page.setDefaultNavigationTimeout(60000);
          await page.goto("https://magpie.test/?view=providers");
          await page.locator(".row.provider", { hasText: data.providers[0].name }).first().click();
          await page.locator(".editor .accts .acc").first().waitFor();
          const [copyLabel, said] = await page.evaluate(() => [t("Copy model reference"), t("Routing alias")]);
          const row = page.locator(".editor .accts .acc").first();
          assert.equal(await row.getByRole("button", { name: said, exact: true }).textContent(), "@work", "the row starts aliased");
          const entry = row.getByRole("button", { name: copyLabel, exact: true }).or(row.getByRole("menuitem", { name: copyLabel, exact: true }));
          await entry.first().waitFor();
          await entry.first().focus();
          await page.keyboard.press("Enter");
          const copied = await page.evaluate(() => navigator.clipboard.readText());
          assert.equal(copied, `${data.providers[0].id}/m@work`, "the clipboard holds the whole reference, the base model as the account serves it, nothing appended");
          assert.deepEqual(writes, [], "copying writes nothing");
          assert.equal(await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth), 0, `${width}px must fit`);
          assert.deepEqual(errors, []);
          await context.close();
        }
      });
    }
  }
}

// The copy entry on the data the backend really answers (providers.go
// RoutingModels): each aliased owner's own complete references under its
// stable row id, two owners with different lists. Several references open
// the model menu; it is opened and chosen from the keyboard, at 360px too,
// the whole chosen reference lands on the real clipboard, and nothing
// leaves the page. The same secure origin and permissions as the copy
// block above; the clipboard stays the real one, read back the same way.
for (const engine of process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"]) {
  for (const lang of ["en", "zh"]) {
    for (const kind of ["account", "key"]) {
      test(`${engine} ${lang}: choosing from several references of an aliased ${kind}`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        for (const width of [1100, 360]) {
          const context = await browser.newContext({ viewport: { width, height: 800 }, reducedMotion: "reduce",
            permissions: engine === "webkit" ? ["clipboard-read"] : ["clipboard-read", "clipboard-write"] });
          const page = await context.newPage(), writes = [], errors = [], data = fixture(kind, 2);
          // the shape /api/providers answers: each owner's own references
          // under its stable row id, and the two owners' lists differ
          const p = data.providers[0], rows = kind === "account" ? p.account.logins : p.keyList;
          p.routingModels = {
            [rows[0].id]: [`${p.id}/m@work`, `${p.id}/m2@work`],
            [rows[1].id]: [`${p.id}/m2@team`, `${p.id}/m3@team`],
          };
          rows[0].routingAlias = "work";
          rows[1].routingAlias = "team";
          if (kind === "account") p.account.routingAlias = "work";
          page.on("pageerror", (e) => errors.push(e.message));
          await page.route("**/*", serveWrites(lang, data, writes));
          page.setDefaultTimeout(5000);
          page.setDefaultNavigationTimeout(60000);
          await page.goto("https://magpie.test/?view=providers");
          await page.locator(".row.provider", { hasText: p.name }).first().click();
          await page.locator(".editor .accts .acc").first().waitFor();
          const [copyLabel, said] = await page.evaluate(() => [t("Copy model reference"), t("Routing alias")]);
          const rowsIn = page.locator(".editor .accts .acc");
          assert.equal(await rowsIn.nth(0).getByRole("button", { name: said, exact: true }).textContent(), "@work", "the first row starts aliased");
          assert.equal(await rowsIn.nth(1).getByRole("button", { name: said, exact: true }).textContent(), "@team", "the second row starts aliased");
          // the keyboard alone: focus, Enter opens the menu of this owner's
          // references, ArrowDown to the second, Enter copies it whole
          const entry = rowsIn.nth(0).getByRole("button", { name: copyLabel, exact: true });
          await entry.focus();
          await page.keyboard.press("Enter");
          const menu = page.locator(".proto-menu");
          await menu.waitFor();
          assert.deepEqual(await menu.locator(".pm-name").allTextContents(),
            [`${p.id}/m@work`, `${p.id}/m2@work`], "the menu offers this owner's references alone, the whole of them");
          await page.keyboard.press("ArrowDown");
          await page.keyboard.press("Enter");
          await menu.waitFor({ state: "detached" });
          const copied = await page.evaluate(() => navigator.clipboard.readText());
          assert.equal(copied, `${p.id}/m2@work`, "the clipboard holds the whole chosen reference, nothing appended");
          // the other owner's menu holds its own different list; opened and
          // left with Escape, it copies nothing
          await rowsIn.nth(1).getByRole("button", { name: copyLabel, exact: true }).focus();
          await page.keyboard.press("Enter");
          await menu.waitFor();
          assert.deepEqual(await menu.locator(".pm-name").allTextContents(),
            [`${p.id}/m2@team`, `${p.id}/m3@team`], "the second owner's menu holds its own different list");
          await page.keyboard.press("Escape");
          await menu.waitFor({ state: "detached" });
          assert.equal(await page.evaluate(() => navigator.clipboard.readText()), `${p.id}/m2@work`, "leaving the second owner's menu copies nothing");
          assert.deepEqual(writes, [], "copying and the menus write nothing");
          assert.equal(await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth), 0, `${width}px must fit`);
          assert.deepEqual(errors, []);
          await context.close();
        }
      });
    }
  }
}

// An owner serving no model (design §5: references derive from served
// entries, not a key's plan): its row keeps its alias but has no list of
// its own in routingModels — the backend leaves it out — so its copy
// button is disabled, and neither the keyboard nor another owner's
// reference can be copied through it. The first owner's single reference
// still copies at once, from the real routingModels data.
for (const engine of process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"]) {
  for (const lang of ["en", "zh"]) {
    for (const kind of ["account", "key"]) {
      test(`${engine} ${lang}: no copy from an aliased ${kind} serving no model`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        for (const width of [1100, 360]) {
          const context = await browser.newContext({ viewport: { width, height: 800 }, reducedMotion: "reduce",
            permissions: engine === "webkit" ? ["clipboard-read"] : ["clipboard-read", "clipboard-write"] });
          const page = await context.newPage(), writes = [], errors = [], data = fixture(kind, 2);
          const p = data.providers[0], rows = kind === "account" ? p.account.logins : p.keyList;
          // the second owner serves nothing: absent from routingModels, as
          // the backend answers it, never an empty list of another owner's
          p.routingModels = { [rows[0].id]: [`${p.id}/m@work`] };
          rows[0].routingAlias = "work";
          rows[1].routingAlias = "team";
          if (kind === "account") p.account.routingAlias = "work";
          page.on("pageerror", (e) => errors.push(e.message));
          await page.route("**/*", serveWrites(lang, data, writes));
          page.setDefaultTimeout(5000);
          page.setDefaultNavigationTimeout(60000);
          await page.goto("https://magpie.test/?view=providers");
          await page.locator(".row.provider", { hasText: p.name }).first().click();
          await page.locator(".editor .accts .acc").first().waitFor();
          const [copyLabel, said] = await page.evaluate(() => [t("Copy model reference"), t("Routing alias")]);
          const rowsIn = page.locator(".editor .accts .acc");
          // the first owner's one reference copies at once, from the real data
          await rowsIn.nth(0).getByRole("button", { name: copyLabel, exact: true }).focus();
          await page.keyboard.press("Enter");
          assert.equal(await page.evaluate(() => navigator.clipboard.readText()), `${p.id}/m@work`, "a single reference copies whole");
          // the owner serving no model: aliased, its copy entry disabled
          assert.equal(await rowsIn.nth(1).getByRole("button", { name: said, exact: true }).textContent(), "@team", "the row keeps its alias");
          const none = rowsIn.nth(1).getByRole("button", { name: copyLabel, exact: true });
          assert.equal(await none.count(), 1, "the row without servable models still has the copy entry");
          assert.equal(await none.isDisabled(), true, "an owner serving no model has nothing to copy");
          // no keyboard path reaches it, and no other owner's reference
          // comes out of it
          await page.keyboard.press("Enter");
          await page.keyboard.press(" ");
          assert.equal(await page.evaluate(() => navigator.clipboard.readText()), `${p.id}/m@work`, "the disabled entry copied nothing, least of all another owner's reference");
          assert.deepEqual(writes, [], "nothing was written");
          assert.equal(await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth), 0, `${width}px must fit`);
          assert.deepEqual(errors, []);
          await context.close();
        }
      });
    }
  }
}

// An aliased account's first line is a row, not a page: the account tests
// above check the page's own scrollWidth, but .accts has overflow:hidden,
// so a line wider than its row is cut off without moving the page. This
// renders two subscription accounts by weight (Max and Pro, stable login
// ids distinct from their emails) and checks the row itself: row and
// .acc-line scrollWidth against clientWidth, the edit entry's 22px desktop
// box (44px on a phone is the existing touch height), the 32-character
// active @alias, Copy model reference, and the in-place input all have a
// box inside the row and a point a pointer can hit. The edit and copy
// entries are exercised through the keyboard first, so a geometry failure
// is about layout, not an unreachable or faked control. English and
// Japanese are enough for the wide and narrow labels; both engines.
function rowFitFixture(alias) {
  return { providers: [{
    id: "claude", name: "Claude Code", icon: "claude-color", chat: "", responses: "", anthropic: "", catalog: "", routing: "",
    models: [{ id: "claude-opus-5-5", name: "Claude Opus 5.5", on: true }], agents: [], fallback: [], headers: {}, keyList: [],
    account: { agent: "claude", agentName: "Claude Code", user: "max@example.com", plan: "max", routingAlias: alias,
      logins: [
        { id: "login-max", user: "max@example.com", plan: "max", active: true, on: true, routingAlias: alias },
        { id: "login-pro", user: "pro@example.com", plan: "pro", on: true, routingAlias: "" },
      ] },
    accountWeights: { "max@example.com": 3, "pro@example.com": 1 },
  }], presets: [], excluded: [], gateway: { running: true, window: true } };
}
// Record every POST the page makes, including the alias save; serve() still
// answers the alias route and redraws from the same provider object.
function serveRowFit(lang, data, posts) {
  const inner = serve(lang, data, []);
  return async (route) => {
    const req = route.request(), u = new URL(req.url());
    if (req.method() === "POST") posts.push({ path: u.pathname, body: req.postData() ? JSON.parse(req.postData()) : null });
    return inner(route);
  };
}
async function rowControl(control) {
  await control.scrollIntoViewIfNeeded();
  return control.evaluate((el) => {
    const b = el.getBoundingClientRect(), r = el.closest(".acc").getBoundingClientRect();
    const mid = document.elementFromPoint(b.x + b.width / 2, b.y + b.height / 2);
    return {
      left: b.left, right: b.right, top: b.top, bottom: b.bottom, width: b.width, height: b.height,
      rowLeft: r.left, rowRight: r.right,
      inRow: b.left >= r.left - 1 && b.right <= r.right + 1,
      hit: !!mid && (mid === el || el.contains(mid)),
    };
  });
}
async function rowFit(page) {
  return page.evaluate(() => [...document.querySelectorAll(".editor .accts .acc[data-account-id]")].map((r) => {
    const line = r.querySelector(".acc-line");
    return { who: r.dataset.accountId, rowOver: r.scrollWidth - r.clientWidth, lineOver: line.scrollWidth - line.clientWidth };
  }));
}
const rowAlias32 = "a".repeat(32);
for (const engine of process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"]) {
  for (const lang of ["en", "ja"]) {
    for (const state of ["empty", "active32"]) {
      test(`${engine} ${lang} ${state}: an account row by weight keeps its alias, copy and editor in the row`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        for (const width of [1100, 360]) {
          const context = await browser.newContext({ viewport: { width, height: 900 }, reducedMotion: "reduce",
            permissions: engine === "webkit" ? ["clipboard-read"] : ["clipboard-read", "clipboard-write"] });
          const page = await context.newPage(), posts = [], errors = [], data = rowFitFixture(state === "active32" ? rowAlias32 : "");
          page.setDefaultTimeout(5000);
          page.setDefaultNavigationTimeout(60000);
          page.on("pageerror", (e) => errors.push(e.message));
          await page.route("**/*", serveRowFit(lang, data, posts));
          await page.goto("https://magpie.test/?view=providers");
          await page.locator(".row.provider", { hasText: "Claude Code" }).first().click();
          const rows = page.locator(".editor .accts .acc[data-account-id]");
          await rows.first().waitFor();
          assert.equal(await rows.count(), 2, "both subscription accounts are drawn");
          const maxRow = page.locator('.editor .accts .acc[data-account-id="max@example.com"]');
          const proRow = page.locator('.editor .accts .acc[data-account-id="pro@example.com"]');
          assert.equal((await maxRow.locator(".plan").textContent()).trim(), "Claude Max", "the first account is the Max plan");
          assert.equal((await proRow.locator(".plan").textContent()).trim(), "Claude Pro", "the second is the Pro plan");
          const by = await page.evaluate(() => t("By weight"));
          await page.locator(".editor").getByRole("button", { name: by, exact: true }).click();
          await page.locator(".editor .acc .key-weight").first().waitFor();
          assert.equal(await page.locator(".editor .acc .key-weight").count(), 2, "both accounts are weighted");
          const label = await page.evaluate(() => t("Routing alias"));
          const copyLabel = await page.evaluate(() => t("Copy model reference"));
          const aliasButton = maxRow.getByRole("button", { name: label, exact: true });
          assert.equal(await aliasButton.count(), 1, "the Max row has its routing-alias entry");

          if (state === "empty") {
            // prove the legitimate edit entry before judging its box
            assert.equal((await aliasButton.textContent()).trim(), label, "an empty alias shows its localized accessible label");
            const emptyBox = await rowControl(aliasButton);
            const emptyFit = await rowFit(page);
            await aliasButton.focus();
            await page.keyboard.press("Enter");
            const input = maxRow.locator("input.rename-in");
            await input.waitFor();
            assert.equal(await input.getAttribute("aria-label"), label, "the in-place editor keeps the accessible name");
            const inputBox = await rowControl(input);
            await input.fill(rowAlias32);
            await input.press("Enter");
            await page.waitForFunction((said) => [...document.querySelectorAll(".editor .accts .acc")].some((r) => r.textContent.includes(said)), `@${rowAlias32}`);
            const saved = posts.filter((p) => p.path === "/api/provider/accountalias");
            assert.equal(saved.length, 1, "saving the editor posts once");
            assert.deepEqual(saved[0].body, { id: "claude", account: "login-max", alias: rowAlias32 }, "the save names the stable login id, not the email or display name");
            assert.ok(emptyBox.width > 0 && emptyBox.height > 0, "the empty entry has a box");
            assert.equal(Math.round(emptyBox.width), 22, "the empty alias is 22px wide on desktop and phone");
            assert.ok(emptyBox.height >= 21 && emptyBox.height <= (width === 1100 ? 24 : 48), `the empty alias keeps the platform's legal height (${Math.round(emptyBox.height)}px)`);
            assert.equal(emptyBox.inRow, true, "the empty entry stays inside the row");
            assert.equal(emptyBox.hit, true, "the empty entry is hit by a pointer at its middle");
            assert.equal(inputBox.inRow, true, "the editor stays inside the row");
            assert.equal(inputBox.hit, true, "the editor is hit by a pointer at its middle");
            for (const f of emptyFit) {
              assert.ok(f.rowOver <= 1, `${f.who}'s row does not scroll sideways (${f.rowOver}px)`);
              assert.ok(f.lineOver <= 1, `${f.who}'s first line does not scroll sideways (${f.lineOver}px)`);
            }
          } else {
            // prove the active alias and Copy entry through the keyboard
            assert.equal((await aliasButton.textContent()).trim(), `@${rowAlias32}`, "the active alias is shown whole");
            const copyButton = maxRow.getByRole("button", { name: copyLabel, exact: true });
            assert.equal(await copyButton.count(), 1, "an active alias has a Copy model reference entry");
            await copyButton.focus();
            await page.keyboard.press("Enter");
            assert.equal(await page.evaluate(() => navigator.clipboard.readText()), `claude/claude-opus-5-5@${rowAlias32}`, "Copy writes the whole qualified reference");
            assert.deepEqual(posts, [], "Copy writes nothing");
            await aliasButton.focus();
            await page.keyboard.press("Enter");
            const input = maxRow.locator("input.rename-in");
            await input.waitFor();
            assert.equal(await input.inputValue(), rowAlias32, "the active alias opens for editing from the keyboard");
            const inputBox = await rowControl(input);
            await page.keyboard.press("Escape");
            assert.equal(await input.count(), 0, "Escape leaves the editor");
            const aliasBox = await rowControl(aliasButton);
            const copyBox = await rowControl(copyButton);
            assert.equal(aliasBox.inRow, true, "the active @alias stays inside the row");
            assert.equal(aliasBox.hit, true, "the active @alias is hit by a pointer at its middle");
            assert.equal(copyBox.inRow, true, "Copy model reference stays inside the row");
            assert.equal(copyBox.hit, true, "Copy model reference is hit by a pointer at its middle");
            assert.equal(inputBox.inRow, true, "the active editor stays inside the row");
            assert.equal(inputBox.hit, true, "the active editor is hit by a pointer at its middle");
            for (const f of await rowFit(page)) {
              assert.ok(f.rowOver <= 1, `${f.who}'s row does not scroll sideways (${f.rowOver}px)`);
              assert.ok(f.lineOver <= 1, `${f.who}'s first line does not scroll sideways (${f.lineOver}px)`);
            }
          }
          assert.deepEqual(errors, [], "no page error");
          await context.close();
        }
      });
    }
  }
}
