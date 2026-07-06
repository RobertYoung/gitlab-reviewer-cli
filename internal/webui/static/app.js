// gitlab-reviewer GUI glue: SSE streams, theme toggle, reload-free findings
// triage, diff keyboard navigation and viewed-file tracking, toasts, and the
// inline comment form. Everything degrades to plain forms and links.
(function () {
  "use strict";

  var $ = function (sel, root) { return (root || document).querySelector(sel); };
  var $$ = function (sel, root) { return Array.prototype.slice.call((root || document).querySelectorAll(sel)); };

  // --- toasts ---------------------------------------------------------
  function toast(message, kind) {
    var host = $("#toast-host");
    if (!host) return;
    var el = document.createElement("div");
    el.className = "alert-msg" + (kind ? " " + kind : "");
    el.textContent = message;
    host.appendChild(el);
    setTimeout(function () { el.remove(); }, 4000);
  }

  // --- theme toggle -----------------------------------------------------
  var themeBtn = $("#theme-toggle");
  if (themeBtn) {
    themeBtn.addEventListener("click", function () {
      var next = document.documentElement.dataset.theme === "light" ? "dark" : "light";
      document.documentElement.dataset.theme = next;
      try { localStorage.setItem("theme", next); } catch (e) { /* storage disabled */ }
    });
  }

  // --- keyboard help overlay -------------------------------------------
  // Populated per page below; [key, description] pairs.
  var shortcuts = [];
  function addShortcuts(pairs) { shortcuts = shortcuts.concat(pairs); }
  function typing(ev) {
    var t = ev.target;
    return t && (t.tagName === "TEXTAREA" || t.tagName === "INPUT" || t.tagName === "SELECT" || t.isContentEditable);
  }
  function showHelp() {
    var dialog = $("#help-dialog");
    var body = $("#help-body");
    if (!dialog || !body) return;
    var table = document.createElement("table");
    shortcuts.concat([["?", "show this help"]]).forEach(function (s) {
      var tr = document.createElement("tr");
      var kbd = document.createElement("kbd");
      kbd.textContent = s[0];
      var keyCell = document.createElement("td");
      keyCell.appendChild(kbd);
      var descCell = document.createElement("td");
      descCell.textContent = s[1];
      tr.appendChild(keyCell);
      tr.appendChild(descCell);
      table.appendChild(tr);
    });
    body.replaceChildren(table);
    dialog.showModal();
  }
  document.addEventListener("keydown", function (ev) {
    if (ev.key === "?" && !typing(ev) && shortcuts.length) {
      ev.preventDefault();
      showHelp();
    }
  });

  // --- review run page: stream progress, then jump to the findings ------
  var runPage = $(".runpage");
  if (runPage) {
    var log = $("#runlog");

    // Per-agent progress chips, derived from the run log's "[agent] text"
    // line grammar (see runner.go): first sight means running, "done: N
    // finding(s)" and "failed: …" are terminal.
    var strip = $("#agent-strip");
    var agents = new Map();
    var feedAgentStrip = function (line) {
      var m = line.match(/^\s*\S+\s+\[([^\]]+)\]\s*(.*)$/);
      if (!m) return;
      var name = m[1], text = m[2];
      var st = agents.get(name) || { status: "running", findings: null };
      var done = text.match(/^done: (\d+) finding/);
      if (done) {
        st.status = "done";
        st.findings = done[1];
      } else if (text.indexOf("failed:") === 0) {
        st.status = "failed";
      }
      agents.set(name, st);
      renderAgentStrip();
    };
    var renderAgentStrip = function () {
      if (!strip || agents.size === 0) return;
      strip.hidden = false;
      strip.replaceChildren();
      agents.forEach(function (st, name) {
        var chip = document.createElement("span");
        chip.className = "agent-chip " + st.status;
        if (st.status === "running") {
          var spin = document.createElement("span");
          spin.className = "spinner";
          chip.appendChild(spin);
        } else {
          var svg = document.createElementNS("http://www.w3.org/2000/svg", "svg");
          svg.setAttribute("class", "icon");
          var use = document.createElementNS("http://www.w3.org/2000/svg", "use");
          use.setAttribute("href", st.status === "done" ? "#i-check" : "#i-cross");
          svg.appendChild(use);
          chip.appendChild(svg);
        }
        chip.appendChild(document.createTextNode(" " + name));
        if (st.findings !== null && st.status === "done") {
          var n = document.createElement("span");
          n.className = "findings";
          n.textContent = " · " + st.findings;
          chip.appendChild(n);
        }
        strip.appendChild(chip);
      });
    };
    // Parse the server-rendered snapshot (finished runs have no stream).
    if (log) log.textContent.split("\n").forEach(feedAgentStrip);

    if (!runPage.dataset.done) {
      var es = new EventSource(runPage.dataset.events);
      // The stream replays the full history on every (re)connect; drop the
      // server-rendered snapshot so lines are not duplicated.
      es.onopen = function () {
        log.textContent = "";
      };
      es.addEventListener("line", function (ev) {
        var line = JSON.parse(ev.data);
        log.textContent += line + "\n";
        log.scrollTop = log.scrollHeight;
        feedAgentStrip(line);
      });
      es.addEventListener("done", function (ev) {
        es.close();
        var out = JSON.parse(ev.data);
        if (!out.error && out.findingsUrl && !out.draftReady) {
          window.location.replace(out.findingsUrl);
        } else {
          // Cancelled, failed, or a draft review awaits its one-click
          // publish: reload so the server renders the outcome.
          window.location.reload();
        }
      });
      es.onerror = function () {
        // Server gone (ctrl+c) — stop retrying, leave the log on screen.
        if (es.readyState === EventSource.CLOSED) return;
      };
    }
  }

  // --- chat page: stream the pending reply's progress, then re-render ---
  var chatPage = $(".chatpage");
  if (chatPage) {
    if (!chatPage.dataset.idle) {
      var status = $("#chatstatus");
      var ces = new EventSource(chatPage.dataset.events);
      ces.onopen = function () {
        if (status) status.textContent = "";
      };
      ces.addEventListener("line", function (ev) {
        if (!status) return;
        status.textContent += JSON.parse(ev.data) + "\n";
        status.scrollTop = status.scrollHeight;
      });
      ces.addEventListener("done", function () {
        ces.close();
        window.location.reload();
      });
      ces.onerror = function () {
        if (ces.readyState === EventSource.CLOSED) return;
      };
    }
    addShortcuts([["⌘/ctrl+enter", "send the message"]]);
    document.addEventListener("keydown", function (ev) {
      // ctrl/cmd+enter sends the message, like the TUI's ctrl+s.
      if ((ev.ctrlKey || ev.metaKey) && ev.key === "Enter") {
        var form = ev.target.closest("form.chat-form");
        if (form) form.submit();
      }
    });
  }

  // --- findings triage: fetch-driven curation, shared with the diff -----
  // Cards are [data-finding] articles; accept/reject forms are
  // .f-state-form, edits .f-edit-form, plus #accept-all-form. Responses
  // carry every finding's state and the totals.
  function stateBadgeFor(card) {
    return $(".badge.fstate", card);
  }
  function applyStates(states) {
    $$("[data-finding]").forEach(function (card) {
      var st = states[card.dataset.finding];
      if (!st) return;
      card.className = card.className.replace(/\bstate-\S+/g, "").trim() + " state-" + st;
      card.dataset.state = st;
      var badge = stateBadgeFor(card);
      if (badge) {
        badge.className = "badge fstate state-" + st;
        badge.textContent = st;
      }
    });
  }
  function applyCounts(out) {
    var set = function (id, n) {
      var el = $(id);
      if (el) el.textContent = n;
    };
    set("#count-accepted", out.accepted);
    set("#count-rejected", out.rejected);
    set("#count-pending", out.pending);
    set("#count-publish", out.accepted);
    var publish = $("#publish-button");
    if (publish) publish.classList.toggle("disabled", out.accepted === 0);
  }
  function postState(form, onDone) {
    var data = new URLSearchParams(new FormData(form));
    data.set("format", "json");
    // form.action is shadowed by the <input name="action"> control, so read
    // the URL from the attribute instead of the (element-valued) property.
    fetch(form.getAttribute("action"), { method: "POST", body: data })
      .then(function (res) {
        if (!res.ok) throw new Error("HTTP " + res.status);
        return res.json();
      })
      .then(function (out) {
        applyStates(out.states);
        applyCounts(out);
        if (onDone) onDone(out);
      })
      .catch(function (err) {
        toast("saving failed: " + err.message + " — reloading", "error");
        setTimeout(function () { window.location.reload(); }, 1200);
      });
  }
  document.addEventListener("submit", function (ev) {
    var form = ev.target;
    if (form.classList.contains("f-state-form") || form.id === "accept-all-form") {
      ev.preventDefault();
      postState(form);
    } else if (form.classList.contains("f-edit-form")) {
      ev.preventDefault();
      postState(form, function (out) {
        var card = form.closest("[data-finding]");
        var body = card && $("pre.prose", card);
        if (body && out.body) body.textContent = out.body;
        var details = form.closest("details");
        if (details) details.open = false;
        toast("finding updated", "ok");
      });
    }
  });

  // Findings page: keyboard triage, filter chips.
  var findingsPage = $(".findings-page");
  if (findingsPage) {
    var cards = $$("article[data-finding]", findingsPage);
    cards.forEach(function (c) {
      c.dataset.state = (c.className.match(/\bstate-(\S+)/) || [])[1] || "pending";
    });

    var filters = $("#finding-filters");
    if (filters && cards.length > 1) {
      filters.hidden = false;
      var applyFilters = function () {
        var states = $$(".chip.active[data-filter-state]", filters).map(function (c) { return c.dataset.filterState; });
        var sevs = $$(".chip.active[data-filter-sev]", filters).map(function (c) { return c.dataset.filterSev; });
        cards.forEach(function (card) {
          var stateOK = states.length === 0 || states.indexOf(card.dataset.state) >= 0 ||
            (states.indexOf("accepted") >= 0 && card.dataset.state === "published");
          var sevOK = sevs.length === 0 || sevs.indexOf(card.dataset.severity) >= 0;
          card.classList.toggle("filtered", !(stateOK && sevOK));
        });
      };
      filters.addEventListener("click", function (ev) {
        var chip = ev.target.closest(".chip");
        if (!chip) return;
        chip.classList.toggle("active");
        applyFilters();
      });
    }

    var visible = function () {
      return cards.filter(function (c) { return !c.classList.contains("filtered"); });
    };
    var focused = -1;
    var focusCard = function (idx) {
      var vis = visible();
      if (vis.length === 0) return;
      idx = Math.max(0, Math.min(idx, vis.length - 1));
      cards.forEach(function (c) { c.classList.remove("kfocus"); });
      vis[idx].classList.add("kfocus");
      vis[idx].scrollIntoView({ block: "center" });
      focused = idx;
    };
    var focusedCard = function () {
      return visible()[focused] || null;
    };
    var submitAction = function (card, action) {
      if (!card) return;
      var forms = $$(".f-state-form", card);
      for (var i = 0; i < forms.length; i++) {
        var input = forms[i].querySelector('input[name="action"]');
        if (input && input.value === action) {
          postState(forms[i]);
          return;
        }
      }
    };
    addShortcuts([
      ["j / k", "next / previous finding"],
      ["a", "accept the focused finding"],
      ["x", "reject the focused finding"],
      ["e", "edit the focused finding's body"],
      ["A", "accept all pending findings"],
    ]);
    document.addEventListener("keydown", function (ev) {
      if (typing(ev) || ev.metaKey || ev.ctrlKey || ev.altKey) return;
      switch (ev.key) {
        case "j":
          focusCard(focused + 1);
          break;
        case "k":
          focusCard(focused - 1);
          break;
        case "a":
          submitAction(focusedCard(), "accept");
          break;
        case "x":
          submitAction(focusedCard(), "reject");
          break;
        case "e":
          var card = focusedCard();
          if (card) {
            var edit = $("details.edit", card);
            if (edit) {
              edit.open = !edit.open;
              if (edit.open) $("textarea", edit).focus();
            }
          }
          break;
        case "A":
          var all = $("#accept-all-form");
          if (all) postState(all);
          break;
      }
    });
  }

  // --- diff view --------------------------------------------------------
  var diffLayout = $(".diff-layout");
  var tmpl = $("#comment-form-template");
  if (diffLayout) {
    // Keyboard navigation across hunks and files. Jump targets sit below
    // the sticky topbar + file header (~90px).
    var OFFSET = 96;
    var jump = function (targets, backwards) {
      var next = null;
      for (var i = 0; i < targets.length; i++) {
        var top = targets[i].getBoundingClientRect().top;
        if (!backwards && top > OFFSET + 4) { next = targets[i]; break; }
        if (backwards && top < OFFSET - 4) next = targets[i];
      }
      if (next) window.scrollBy({ top: next.getBoundingClientRect().top - OFFSET });
    };
    addShortcuts([
      ["] / [", "next / previous hunk"],
      ["n / p", "next / previous file"],
      ["esc", "close the comment form"],
      ["⌘/ctrl+enter", "submit the open comment form"],
    ]);
    document.addEventListener("keydown", function (ev) {
      if (typing(ev) || ev.metaKey || ev.ctrlKey || ev.altKey) return;
      switch (ev.key) {
        case "]":
          jump($$(".diff-files tr.hunk"), false);
          break;
        case "[":
          jump($$(".diff-files tr.hunk"), true);
          break;
        case "n":
          jump($$(".diff-files .diff-file"), false);
          break;
        case "p":
          jump($$(".diff-files .diff-file"), true);
          break;
      }
    });

    // Viewed-file tracking, persisted per MR head so a new push resets it.
    var viewedKey = diffLayout.dataset.viewedKey;
    var loadViewed = function () {
      try { return new Set(JSON.parse(localStorage.getItem(viewedKey) || "[]")); } catch (e) { return new Set(); }
    };
    var viewed = loadViewed();
    var saveViewed = function () {
      try { localStorage.setItem(viewedKey, JSON.stringify(Array.from(viewed))); } catch (e) { /* storage disabled */ }
    };
    var files = $$(".diff-file", diffLayout);
    var updateViewedUI = function () {
      files.forEach(function (article) {
        var isViewed = viewed.has(article.dataset.path);
        article.classList.toggle("viewed", isViewed);
        article.classList.toggle("collapsed", isViewed || article.classList.contains("folded"));
        var box = $(".viewed-box", article);
        if (box) box.checked = isViewed;
      });
      $$(".filetree li.file-entry").forEach(function (li) {
        li.classList.toggle("viewed", viewed.has(li.dataset.path));
      });
      var progress = $("#viewed-progress");
      if (progress && files.length) {
        var n = files.filter(function (f) { return viewed.has(f.dataset.path); }).length;
        progress.textContent = n ? n + "/" + files.length + " viewed" : "";
      }
    };
    updateViewedUI();

    diffLayout.addEventListener("change", function (ev) {
      var box = ev.target.closest(".viewed-box");
      if (!box) return;
      var article = box.closest(".diff-file");
      if (box.checked) {
        viewed.add(article.dataset.path);
      } else {
        viewed.delete(article.dataset.path);
        article.classList.remove("folded");
      }
      saveViewed();
      updateViewedUI();
    });
    diffLayout.addEventListener("click", function (ev) {
      var header = ev.target.closest(".diff-file > header");
      if (!header || ev.target.closest("label, input, a")) return;
      var article = header.closest(".diff-file");
      // Manual fold is independent of viewed, but unfolding un-collapses.
      var collapsed = article.classList.toggle("collapsed");
      article.classList.toggle("folded", collapsed);
      if (!collapsed) {
        var box = $(".viewed-box", article);
        if (box && box.checked) {
          box.checked = false;
          viewed.delete(article.dataset.path);
          saveViewed();
          updateViewedUI();
        }
      }
    });
  }

  // One floating comment form, moved to the clicked line.
  if (tmpl) {
    var open = null; // the currently inserted <tr>

    var close = function () {
      if (open) {
        open.remove();
        open = null;
      }
    };

    document.addEventListener("click", function (ev) {
      var cancel = ev.target.closest(".cancel-comment");
      if (cancel) {
        close();
        return;
      }
      var btn = ev.target.closest(".add-comment");
      if (!btn) return;
      var row = btn.closest("tr.line");
      if (!row) return;
      close();

      var frag = tmpl.content.cloneNode(true);
      var tr = frag.querySelector("tr");
      var form = frag.querySelector("form");
      // Split-view buttons carry their own side's anchor; unified rows
      // carry it on the row.
      var src = btn.dataset.file !== undefined ? btn.dataset : row.dataset;
      form.querySelector('[name="file"]').value = src.file || "";
      form.querySelector('[name="old"]').value = src.old || "";
      form.querySelector('[name="new"]').value = src.new || "";
      // The form spans the code columns whatever the table layout.
      var tds = tr.querySelectorAll("td");
      tds[0].colSpan = row.cells.length === 6 ? 2 : 3;
      tds[1].colSpan = row.cells.length === 6 ? 4 : 1;
      row.after(tr);
      open = tr;
      tr.querySelector("textarea").focus();
    });

    document.addEventListener("keydown", function (ev) {
      if (ev.key === "Escape") close();
      // ctrl/cmd+enter submits the open comment form, like the TUI's ctrl+s.
      if ((ev.ctrlKey || ev.metaKey) && ev.key === "Enter" && open) {
        open.querySelector("form").submit();
      }
    });
  }

  // Unhide the topbar help button on pages that registered shortcuts.
  var helpBtn = $("#help-toggle");
  if (helpBtn && shortcuts.length) {
    helpBtn.hidden = false;
    helpBtn.addEventListener("click", showHelp);
  }
})();
