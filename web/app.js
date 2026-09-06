(() => {
  const state = {
    mode: "markdown",
    theme: "default",
    timer: 0,
    toastTimer: 0,
    html: "",
    copied: false,
    richSeeded: false,
    emphasisCount: 0
  };

  const $ = (selector) => document.querySelector(selector);
  const $$ = (selector) => [...document.querySelectorAll(selector)];
  const markdown = $("#markdown");
  const rich = $("#rich");
  const preview = $("#preview");
  const status = $("#status");

  function setStatus(message, error = false) {
    status.textContent = message;
    status.style.color = error ? "#b42318" : "";
  }

  function showToast(message, error = false) {
    const toast = $("#toast");
    clearTimeout(state.toastTimer);
    toast.textContent = message;
    toast.classList.toggle("error", error);
    toast.classList.add("show");
    state.toastTimer = setTimeout(() => toast.classList.remove("show"), 2200);
  }

  async function postJSON(endpoint, payload) {
    const response = await fetch(endpoint, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(payload)
    });
    const result = await response.json();
    if (!response.ok) throw new Error(result.error || "请求处理失败");
    return result;
  }

  async function render() {
    clearTimeout(state.timer);
    const endpoint = state.mode === "markdown" ? "/api/render" : "/api/inline";
    const content = state.mode === "markdown" ? markdown.value : rich.innerHTML;
    try {
      const result = await postJSON(endpoint, {
        content,
        theme: state.theme,
        emphasis: $("#auto-emphasis").checked
      });
      state.html = result.html;
      state.emphasisCount = result.emphasis || 0;
      state.copied = false;
      preview.innerHTML = result.html;
      updateEmphasisSummary();
      updateStats();
      updateProgress();
      setStatus("预览已更新 · 已生成全内联样式");
    } catch (error) {
      setStatus(error.message, true);
    }
  }

  function scheduleRender() {
    clearTimeout(state.timer);
    state.timer = setTimeout(render, 180);
    updateEditorCount();
  }

  function currentPlainText() {
    return state.mode === "markdown" ? markdown.value : rich.innerText;
  }

  function characterCount(text) {
    return [...text.replace(/\s/g, "")].length;
  }

  function escapeHTML(text) {
    const node = document.createElement("div");
    node.textContent = text;
    return node.innerHTML;
  }

  function updateEditorCount() {
    $("#editor-count").textContent = `${characterCount(currentPlainText())} 字`;
  }

  function updateStats() {
    const text = preview.innerText || "";
    const paragraphs = preview.querySelectorAll("p, li, blockquote").length;
    const headings = preview.querySelectorAll("h1, h2, h3, h4, h5, h6").length;
    const images = preview.querySelectorAll("img").length;
    $("#stat-chars").textContent = characterCount(text);
    $("#stat-paragraphs").textContent = paragraphs;
    $("#stat-headings").textContent = headings;
    $("#stat-images").textContent = images;
    updateEditorCount();
  }

  function updateProgress() {
    const hasContent = characterCount(currentPlainText()) > 0;
    const score = state.copied ? 5 : (hasContent && state.html ? 4 : 1);
    $("#progress-score").textContent = score;
    $("#progress-bar").style.width = `${score * 20}%`;
    const chip = $("#publish-chip");
    chip.textContent = state.copied ? "✓ 发布" : "○ 发布";
    chip.classList.toggle("done", state.copied);
  }

  function updateRecognition(result) {
    const box = $("#structure-summary");
    box.innerHTML = `<span>✓</span><p><b>已完成结构识别</b><small>${result.headings} 个标题 · ${result.paragraphs} 个段落 · ${result.lists} 个列表项</small></p>`;
  }

  function updateEmphasisSummary() {
    const enabled = $("#auto-emphasis").checked;
    const box = $("#emphasis-summary");
    if (!enabled) {
      box.innerHTML = "<span>—</span><p><b>精彩句识别已关闭</b><small>不会自动修改任何句子样式</small></p>";
      return;
    }
    box.innerHTML = state.emphasisCount > 0
      ? `<span>◆</span><p><b>已自动标注 ${state.emphasisCount} 句重点</b><small>使用当前主题的强调色与加粗样式</small></p>`
      : "<span>◇</span><p><b>暂未发现需要强调的句子</b><small>普通内容将保持干净，避免过度装饰</small></p>";
  }

  async function structureText(content, output) {
    return postJSON("/api/structure", { content, output });
  }

  async function smartStructureAll() {
    if (state.mode === "rich" && rich.querySelector("img")) {
      const message = "富文本中已有图片。为避免图片丢失，请重新粘贴文字时使用自动识别。";
      setStatus(message, true);
      showToast(message, true);
      return;
    }
    const content = currentPlainText();
    if (!content.trim()) {
      showToast("请先输入或粘贴文章内容", true);
      return;
    }
    try {
      const output = state.mode === "markdown" ? "markdown" : "html";
      const result = await structureText(content, output);
      if (state.mode === "markdown") markdown.value = result.content;
      else rich.innerHTML = result.content;
      updateRecognition(result);
      setActiveStep("structure");
      await render();
      showToast("已自动识别标题、段落与列表");
    } catch (error) {
      setStatus(error.message, true);
      showToast(error.message, true);
    }
  }

  function setActiveStep(step) {
    $$(".workflow-item").forEach((item) => item.classList.toggle("active", item.dataset.step === step));
  }

  function captureRichRange() {
    const selection = window.getSelection();
    if (!selection || !selection.rangeCount) return null;
    const range = selection.getRangeAt(0);
    return rich.contains(range.commonAncestorContainer) ? range.cloneRange() : null;
  }

  function insertRichHTML(html, savedRange = null) {
    rich.focus();
    const selection = window.getSelection();
    if (savedRange && selection) {
      selection.removeAllRanges();
      selection.addRange(savedRange);
    }
    if (!document.execCommand("insertHTML", false, html)) {
      const range = selection && selection.rangeCount ? selection.getRangeAt(0) : null;
      if (range) {
        const fragment = range.createContextualFragment(html);
        range.deleteContents();
        range.insertNode(fragment);
      } else {
        rich.insertAdjacentHTML("beforeend", html);
      }
    }
    scheduleRender();
  }

  async function handleMarkdownPaste(event) {
    if (!$("#auto-structure").checked) return;
    const text = event.clipboardData?.getData("text/plain") || "";
    if (!text.includes("\n")) return;
    event.preventDefault();
    const start = markdown.selectionStart;
    const end = markdown.selectionEnd;
    try {
      const result = await structureText(text, "markdown");
      markdown.setRangeText(result.content, start, end, "end");
      updateRecognition(result);
      setActiveStep("structure");
      scheduleRender();
      showToast("粘贴内容已自动分段");
    } catch (error) {
      markdown.setRangeText(text, start, end, "end");
      scheduleRender();
      showToast(`自动识别失败，已按原文粘贴：${error.message}`, true);
    }
  }

  async function handleRichPaste(event) {
    if (!$("#auto-structure").checked) return;
    const clipboard = event.clipboardData;
    if (!clipboard) return;
    const imageItem = [...clipboard.items].find((item) => item.kind === "file" && item.type.startsWith("image/"));
    if (imageItem) {
      event.preventDefault();
      const file = imageItem.getAsFile();
      if (!file || file.size > 5 * 1024 * 1024) {
        showToast("粘贴图片不能超过 5 MB", true);
        return;
      }
      const savedRange = captureRichRange();
      const reader = new FileReader();
      reader.onload = () => insertRichHTML(`<img src="${reader.result}" alt="粘贴图片">`, savedRange);
      reader.readAsDataURL(file);
      return;
    }

    const text = clipboard.getData("text/plain");
    const html = clipboard.getData("text/html");
    if (!text && !html) return;
    event.preventDefault();
    const savedRange = captureRichRange();
    try {
      if (html && /<(?:h[1-6]|p|div|ul|ol|blockquote|pre|table|img)\b/i.test(html)) {
        const cleaned = await postJSON("/api/inline", { content: html, theme: state.theme });
        insertRichHTML(cleaned.html, savedRange);
        $("#structure-summary").innerHTML = "<span>✓</span><p><b>已保留富文本结构</b><small>危险标签与外部样式已被清理</small></p>";
      } else {
        const result = await structureText(text, "html");
        insertRichHTML(result.content, savedRange);
        updateRecognition(result);
      }
      setActiveStep("structure");
      showToast("粘贴内容已自动整理");
    } catch (error) {
      insertRichHTML(escapeHTML(text).replace(/\n/g, "<br>"), savedRange);
      showToast(`自动识别失败：${error.message}`, true);
    }
  }

  function chooseTheme(card) {
    state.theme = card.dataset.theme;
    $$(".theme-card").forEach((item) => {
      const selected = item === card;
      item.classList.toggle("active", selected);
      item.querySelector(".theme-name small").textContent = selected ? "已选择" : "选择";
    });
    setActiveStep("theme");
    render();
  }

  async function copyHTML() {
    if (!state.html) {
      showToast("当前没有可复制的内容", true);
      return;
    }
    try {
      if (window.ClipboardItem && navigator.clipboard?.write) {
        const item = new ClipboardItem({
          "text/html": new Blob([state.html], { type: "text/html" }),
          "text/plain": new Blob([preview.innerText], { type: "text/plain" })
        });
        await navigator.clipboard.write([item]);
      } else {
        const range = document.createRange();
        range.selectNodeContents(preview);
        const selection = window.getSelection();
        selection.removeAllRanges();
        selection.addRange(range);
        if (!document.execCommand("copy")) throw new Error("浏览器拒绝了复制请求");
        selection.removeAllRanges();
      }
      state.copied = true;
      updateProgress();
      setActiveStep("publish");
      setStatus("已复制，请粘贴到微信公众号编辑器");
      showToast("排版结果已复制到剪贴板");
      const button = $("#copy");
      const oldText = button.innerHTML;
      button.textContent = "✓ 已复制，可以粘贴";
      setTimeout(() => { button.innerHTML = oldText; }, 1500);
    } catch (error) {
      setStatus(`复制失败：${error.message}`, true);
      showToast(`复制失败：${error.message}`, true);
    }
  }

  function exportHTML() {
    if (!state.html) {
      showToast("当前没有可导出的内容", true);
      return;
    }
    const documentHTML = `<!doctype html><html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>公众号文章</title></head><body><section style="max-width:677px;margin:0 auto;padding:24px;font-family:-apple-system,BlinkMacSystemFont,'PingFang SC','Microsoft YaHei',sans-serif;">${state.html}</section></body></html>`;
    const url = URL.createObjectURL(new Blob([documentHTML], { type: "text/html;charset=utf-8" }));
    const link = document.createElement("a");
    link.href = url;
    link.download = `wechat-article-${new Date().toISOString().slice(0, 10)}.html`;
    link.click();
    setTimeout(() => URL.revokeObjectURL(url), 1000);
    showToast("HTML 文件已导出");
  }

  $$(".tab").forEach((tab) => {
    tab.addEventListener("click", async () => {
      $$(".tab").forEach((item) => item.classList.toggle("active", item === tab));
      if (tab.dataset.mode === "rich" && !state.richSeeded && state.html) {
        rich.innerHTML = state.html;
        state.richSeeded = true;
      }
      state.mode = tab.dataset.mode;
      $("#markdown-wrap").classList.toggle("hidden", state.mode !== "markdown");
      $("#rich-wrap").classList.toggle("hidden", state.mode !== "rich");
      setActiveStep("import");
      render();
    });
  });

  $$(".theme-card").forEach((card) => card.addEventListener("click", () => chooseTheme(card)));

  $$("[data-command]").forEach((button) => {
    button.addEventListener("click", () => {
      rich.focus();
      document.execCommand(button.dataset.command, false);
      scheduleRender();
    });
  });

  $$("[data-block]").forEach((button) => {
    button.addEventListener("click", () => {
      rich.focus();
      document.execCommand("formatBlock", false, button.dataset.block);
      scheduleRender();
    });
  });

  $$(".workflow-item").forEach((item) => {
    item.addEventListener("click", () => {
      const step = item.dataset.step;
      setActiveStep(step);
      if (step === "structure") smartStructureAll();
      else if (step === "theme") $(".theme-strip").scrollIntoView({ behavior: "smooth", block: "nearest" });
      else if (step === "preview") $(".preview-panel").scrollIntoView({ behavior: "smooth", block: "nearest" });
      else if (step === "publish") copyHTML();
      else (state.mode === "markdown" ? markdown : rich).focus();
    });
  });

  $("#image").addEventListener("change", (event) => {
    const file = event.target.files[0];
    if (!file) return;
    if (file.size > 5 * 1024 * 1024) {
      showToast("单张图片请控制在 5 MB 内", true);
      return;
    }
    const reader = new FileReader();
    reader.onload = () => insertRichHTML(`<img src="${reader.result}" alt="本地图片">`);
    reader.readAsDataURL(file);
    event.target.value = "";
  });

  markdown.addEventListener("input", scheduleRender);
  markdown.addEventListener("paste", handleMarkdownPaste);
  rich.addEventListener("input", () => {
    state.richSeeded = true;
    scheduleRender();
  });
  rich.addEventListener("paste", handleRichPaste);
  $("#smart-structure").addEventListener("click", smartStructureAll);
  $("#copy").addEventListener("click", copyHTML);
  $("#top-copy").addEventListener("click", copyHTML);
  $("#export").addEventListener("click", exportHTML);
  $("#auto-structure").addEventListener("change", (event) => {
    $("#structure-summary").innerHTML = event.target.checked
      ? "<span>✦</span><p><b>结构识别已开启</b><small>粘贴多行纯文本即可自动整理</small></p>"
      : "<span>—</span><p><b>结构识别已关闭</b><small>粘贴时将保留原始内容</small></p>";
  });
  $("#auto-emphasis").addEventListener("change", () => {
    render();
    showToast($("#auto-emphasis").checked ? "已开启精彩句自动标注" : "已关闭精彩句自动标注");
  });

  render();
})();
