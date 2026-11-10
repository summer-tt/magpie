// Account routing aliases in the provider editor; loaded when a row is drawn.
window.routingAliasFill = function (wrap, p, a, key) {
  wrap.className = "acct-routing-alias" + (a.routingAlias ? " set" : "");
  const b = el("button", "text quiet", a.routingAlias ? "@" + a.routingAlias : t("Routing alias"));
  b.setAttribute("aria-label", t("Routing alias"));
  b.title = t("Without an alias this account joins ordinary routing. With an alias use provider/model@alias. Clearing it restores ordinary routing; old references fail.");
  b.onclick = (e) => {
    e.stopPropagation();
    const i = input(a.routingAlias || "", t("Routing alias"));
    i.className = "rename-in"; i.maxLength = 32;
    i.setAttribute("aria-label", t("Routing alias"));
    let done = false;
    i.onkeydown = (ev) => {
      ev.stopPropagation();
      if (ev.key === "Escape") { ev.preventDefault(); done = true; wrap.parentNode.classList.toggle("alias", !key && a.routingAlias); i.replaceWith(b); }
      if (ev.key === "Enter" && !done) {
        ev.preventDefault(); done = true;
        accountAction(key ? "keys/alias" : "provider/accountalias", key ? {id:p.id,ref:a.id,alias:i.value} : {id:p.id,account:a.id,alias:i.value}).then((saved) => { if (!saved) done = false; });
      }
    };
    wrap.parentNode.classList.toggle("alias",!key); b.replaceWith(i); i.focus({preventScroll:true});
  };
  wrap.append(b);
  if (a.routingAlias) {
    const allowed = accountModelsOf(p, key ? a.id : a.user);
    const refs = p.routingModels ? (p.routingModels[a.id] || []) : (p.models || []).filter((m) => m.on && !m.noAPI && (!allowed.length || allowed.includes(m.id))).map((m) => `${p.id}/${m.id}@${a.routingAlias}`);
    const cp = el("button", "copy");
    cp.title = t("Copy model reference"); cp.setAttribute("aria-label", cp.title);
    cp.append(svg(COPY_ICON, 12, 1.5)); cp.disabled = !refs.length;
    const put = async (ref) => {
      let copied = false;
      try { if (navigator.clipboard?.writeText) { await navigator.clipboard.writeText(ref); copied = true; } } catch {}
      if (!copied) copied = copyByCommand(ref);
      if (copied) { flashCopied(cp); status(t("{what} copied", {what:t("Model")}), "ok"); }
      else status(t("Couldn't copy. Select it here and copy it yourself: {text}", {text:ref}), "err");
    };
    cp.onclick = (e) => {
      e.stopPropagation();
      if (refs.length === 1) put(refs[0]);
      else openProtoMenu(cp, refs.map((v) => ({v,name:v,note:"",literalName:true})), "", put, "Copy model reference");
    };
    wrap.append(cp);
  }
};
