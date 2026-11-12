// Account access keys in the provider editor, loaded only when needed.
window.planAccessKeyFields = function (wrap, access, pr, draft, presetID, p) {
  let accessAt = () => {}, accessShown = () => true;
  const tc = access.vendor === "tencent", bl = access.vendor === "bailian" || access.vendor === "alibaba" && (presetID === "bailian-token-plan" || [p?.chat, p?.anthropic, p?.responses].some((base) => base?.includes("token-plan.cn-beijing.maas.aliyuncs.com")));
  const idName = tc ? t("SecretId") : t("AccessKey ID"), secretName = tc ? t("SecretKey") : bl ? t("AccessKey Secret") : t("Secret Access Key");
  if (draft.accessKeyID === undefined) draft.accessKeyID = access.id || "";
  const ak = input(draft.accessKeyID, idName);
  ak.classList.add("volc-ak");
  ak.oninput = () => { draft.accessKeyID = ak.value.trim(); };
  const saved = access.secretSet && !draft.clearAccessKey;
  const sk = input(draft.secretAccessKey || "", saved ? t("saved · paste a new one to replace it") : secretName, "password");
  sk.classList.add("volc-sk");
  sk.oninput = () => { draft.secretAccessKey = sk.value.trim(); };
  sk.onkeydown = (e) => { e.stopPropagation(); if (e.key === "Escape") cancelEdit(); };
  const pair = el("div", "pair");
  pair.append(sk);
  if (saved) {
    const side = el("div", "side");
    const drop = el("button", "text", t("Remove"));
    drop.onclick = () => { draft.clearAccessKey = true; draft.accessKeyID = ""; draft.secretAccessKey = ""; ak.value = ""; sk.value = ""; sk.placeholder = secretName; drop.remove(); };
    side.append(drop);
    pair.append(side);
  }
  const rows = [
    ...field(idName, ak, ""),
    ...field(secretName, pair, tc
      ? t("Token Plan Enterprise's usage is told only to the Tencent Cloud account's API key (console → Access management → API keys), not to the plan's API key. Give its SecretId and SecretKey here for the Usage page to show it; a sub-user's key that may read TokenHub is enough. It is used for nothing else.")
      : bl
        ? t("The Bailian Token Plan's monthly usage is told only to the Alibaba Cloud account's access key (RAM console → AccessKeys), not to the plan's sk- key. Give its AccessKey ID and Secret here for the Usage page to show the month's used share and credits; it must be the account the plan is subscribed on, as several sk- keys don't mean several plans. It is used for nothing else.")
      : access.vendor === "alibaba"
      ? t("The plan's usage is told only to the Alibaba Cloud account's AccessKey (console → AccessKey), not to the plan's API key. Give its AccessKey ID and Secret here for the Usage page to show it; a RAM user that may use Model Studio is enough. It is used for nothing else.")
      : t("The plan's 5-hour, weekly and monthly windows are told only to the account's access key (Volcengine console → API Access Keys), not to the plan's API key. Give it here for the Usage page to show them; it is used for nothing else.")),
  ];
  wrap.append(...rows);
  // the plan at some regions only (Tencent's 企业版)
  const at = pr?.accessKeyRegions;
  if (at?.length) {
    accessAt = (r) => {
      const on = !r || at.includes(r.id) || !regionAt(pr, draft);
      for (const x of rows) x.hidden = !on;
    };
    accessShown = () => !rows[0].hidden;
  }
  return { at: accessAt, shown: accessShown };
};
