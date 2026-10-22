// gitlab-reviewer GUI glue: SSE streams, theme toggle, reload-free findings
// triage, diff keyboard navigation and viewed-file tracking, toasts, and the
// inline comment form. Everything degrades to plain forms and links.
(function () {
  "use strict";

  var $ = function (sel, root) { return (root || document).querySelector(sel); };
  var $$ = function (sel, root) { return Array.prototype.slice.call((root || document).querySelectorAll(sel)); };

  function statusIcon(name) {
    var svg = document.createElementNS("http://www.w3.org/2000/svg", "svg");
    svg.setAttribute("class", "icon");
    svg.setAttribute("aria-hidden", "true");
    var use = document.createElementNS("http://www.w3.org/2000/svg", "use");
    use.setAttribute("href", "#i-" + name);
    svg.appendChild(use);
    return svg;
  }

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

  // --- run log: per-agent chips and filtering ---------------------------
  // Shared by the live run page and the stored full-log page. Chips derive
  // from the run log's "[agent] text" line grammar (see runner.go): first
  // sight means running, "done: N finding(s)" and "failed: …" are terminal.
  // Each chip is a toggle that filters the log to the selected agent(s);
  // with none selected every line shows.
  var log = $("#runlog");
  var strip = $("#agent-strip");
  var logAddLine = null;
  var logReset = null;
  if (log && strip) {
    var agents = new Map();
    var selected = new Set();
    var allLines = [];
    var agentOf = function (line) {
      var m = line.match(/^\s*\S+\s+\[([^\]]+)\]/);
      return m ? m[1] : null;
    };
    var lineVisible = function (line) {
      if (selected.size === 0) return true;
      var name = agentOf(line);
      return name !== null && selected.has(name);
    };
    var renderLog = function () {
      var shown = allLines.filter(lineVisible);
      log.textContent = shown.length ? shown.join("\n") + "\n" : "";
    };
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
      if (agents.size === 0) return;
      strip.hidden = false;
      strip.replaceChildren();
      agents.forEach(function (st, name) {
        var chip = document.createElement("button");
        chip.type = "button";
        chip.className = "agent-chip " + st.status + (selected.has(name) ? " selected" : "");
        chip.setAttribute("aria-pressed", selected.has(name) ? "true" : "false");
        chip.title = selected.has(name) ? "Show all agents" : "Show only " + name;
        if (st.status === "running") {
          var spin = document.createElement("span");
          spin.className = "spinner";
          chip.appendChild(spin);
        } else {
          chip.appendChild(statusIcon(st.status === "done" ? "check" : "cross"));
        }
        chip.appendChild(document.createTextNode(" " + name));
        if (st.findings !== null && st.status === "done") {
          var n = document.createElement("span");
          n.className = "findings";
          n.textContent = " · " + st.findings;
          chip.appendChild(n);
        }
        chip.addEventListener("click", function () {
          if (selected.has(name)) selected.delete(name);
          else selected.add(name);
          renderAgentStrip();
          renderLog();
        });
        strip.appendChild(chip);
      });
    };
    logAddLine = function (line) {
      allLines.push(line);
      feedAgentStrip(line);
      if (lineVisible(line)) {
        log.textContent += line + "\n";
        log.scrollTop = log.scrollHeight;
      }
    };
    logReset = function () {
      allLines = [];
      log.textContent = "";
    };
    // Parse the server-rendered snapshot (finished runs and stored logs
    // have no stream).
    allLines = log.textContent.split("\n");
    while (allLines.length && allLines[allLines.length - 1] === "") allLines.pop();
    allLines.forEach(feedAgentStrip);
  }

  // --- review run page: stream progress, then jump to the findings ------
  var runPage = $(".runpage");
  if (runPage) {
    if (!runPage.dataset.done) {
      var es = new EventSource(runPage.dataset.events);
      // The stream replays the full history on every (re)connect; drop the
      // server-rendered snapshot so lines are not duplicated.
      es.onopen = function () {
        if (logReset) logReset();
      };
      es.addEventListener("line", function (ev) {
        if (logAddLine) logAddLine(JSON.parse(ev.data));
      });
      es.addEventListener("done", function (ev) {
        es.close();
        var out = JSON.parse(ev.data);
        loadbarStart();
        if (!out.error && out.findingsUrl && !out.draftReady) {
          window.location.replace(out.findingsUrl);
        } else {
          // Cancelled, failed, or a draft review awaits its one-click
          // publish: reload so the server renders the outcome.
          window.location.reload();
        }
      });
      es.onerror = function () {
        // Server gone (ctrl+c): stop retrying, leave the log on screen.
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
        loadbarStart();
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
        if (form) {
          loadbarStart();
          form.submit();
        }
      }
    });
  }

  // --- findings triage: fetch-driven curation, shared with the diff -----
  // Cards are [data-finding] articles; accept/reject forms are
  // .f-state-form, edits .f-edit-form, plus #accept-all-form. Responses
  // carry every finding's state and the totals.
  function stateBadgeFor(card) {
    return $(".fstate", card);
  }
  function applyStates(states) {
    $$("[data-finding]").forEach(function (card) {
      var st = states[card.dataset.finding];
      if (!st) return;
      card.className = card.className.replace(/\bstate-\S+/g, "").trim() + " state-" + st;
      card.dataset.state = st;
      var badge = stateBadgeFor(card);
      if (badge) {
        badge.className = "pill fstate state-" + st;
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
    updateBand(out);
    if (afterCounts) afterCounts(out);
  }
  var afterCounts = null;

  // The MR band's pipeline follows triage as it happens: the triage and
  // publish steps recount, and the next action moves from triage to publish
  // once nothing is pending. Only when the change hit the record the band
  // reports on.
  function updateBand(out) {
    var band = $(".band[data-record]");
    if (!band || !out.record || band.dataset.record !== out.record) return;
    var total = Object.keys(out.states).length;
    var setStep = function (key, state, text) {
      var step = $('.step[data-step="' + key + '"]', band);
      if (!step) return;
      step.classList.remove("done", "current", "todo");
      step.classList.add(state);
      var node = $(".node", step);
      if (node) {
        if (state === "done") {
          node.replaceChildren(statusIcon("check"));
        } else {
          node.textContent = key === "triage" ? "2" : "3";
        }
      }
      var detail = $(".detail", step);
      if (detail) detail.textContent = text;
    };
    var plural = function (n, noun) { return n === 1 ? "1 " + noun : n + " " + noun + "s"; };
    if (out.pending > 0) {
      setStep("triage", "current", out.pending + " of " + total + " still pending");
    } else {
      setStep("triage", "done", total ? "All " + plural(total, "finding") + " triaged" : "Nothing to triage");
    }
    if (out.accepted > 0) {
      setStep("publish", out.pending > 0 ? "todo" : "current", out.accepted + " accepted, ready to post");
    } else if (out.pending > 0) {
      setStep("publish", "todo", "Nothing accepted yet");
    }
    var next = $(".next-action", band);
    if (!next || next.tagName !== "A") return;
    var label = null, href = null;
    if (out.pending > 0) {
      label = "Continue triage";
      var firstPending = Object.keys(out.states).sort().filter(function (id) { return out.states[id] === "pending"; })[0];
      href = band.dataset.findingsUrl + "#f-" + encodeURIComponent(firstPending);
    } else if (out.accepted > 0) {
      label = "Publish " + out.accepted + " accepted";
      href = band.dataset.publishUrl;
    }
    if (!label) return;
    next.href = href;
    next.replaceChildren(statusIcon(out.pending > 0 ? "arrow-right" : "send"), document.createTextNode(" " + label));
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
        toast("Saving failed (" + err.message + "), reloading", "error");
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

  // Findings page: a list of findings beside one detail pane. Selecting a
  // row shows its finding; j/k move the selection, a/x/e/A triage it, and
  // triaged rows move to their state's group. Without this script every
  // finding is shown at once and the rows are plain anchor links.
  var findingsPage = $(".findings-page");
  if (findingsPage) {
    var triage = $("#triage");
    var cards = $$("article[data-finding]", findingsPage);
    var rows = $$(".triage-row", findingsPage);
    var cardFor = function (id) { return $('article[data-finding="' + id + '"]', findingsPage); };
    var rowFor = function (id) { return $('.triage-row[data-row-for="' + id + '"]', findingsPage); };
    cards.forEach(function (c) {
      c.dataset.state = (c.className.match(/\bstate-(\S+)/) || [])[1] || "pending";
    });
    var groupOf = function (state) { return state === "note" ? "published" : state; };
    // applyStates has already restyled the rows by the time afterCounts runs,
    // so remember each finding's state to tell what a change moved.
    var lastState = {};
    cards.forEach(function (c) { lastState[c.dataset.finding] = c.dataset.state; });

    var selected = null;
    var select = function (id, scroll, keepURL) {
      var card = cardFor(id);
      if (!card) return;
      if (selected) {
        var oldRow = rowFor(selected);
        var oldCard = cardFor(selected);
        if (oldRow) oldRow.classList.remove("selected");
        if (oldCard) oldCard.classList.remove("shown", "kfocus");
      }
      selected = id;
      var row = rowFor(id);
      if (row) {
        row.classList.add("selected");
        if (scroll) row.scrollIntoView({ block: "nearest" });
      }
      card.classList.add("shown", "kfocus");
      // Not while loading: Chrome scrolls to whatever fragment the URL has
      // once parsing finishes, which would jump the page to this card.
      if (!keepURL && history.replaceState) history.replaceState(null, "", "#f-" + id);
    };
    var visibleRows = function () {
      return $$(".triage-row", findingsPage).filter(function (r) { return !r.classList.contains("filtered"); });
    };

    if (triage) {
      triage.classList.add("js");
      var fromHash = (location.hash.match(/^#f-(.+)$/) || [])[1];
      var initial = fromHash && cardFor(fromHash) ? fromHash : null;
      if (!initial) {
        var firstRow = $('.triage-group[data-group="pending"] .triage-row', findingsPage) || rows[0];
        if (firstRow) initial = firstRow.dataset.rowFor;
      }
      if (initial) select(initial, Boolean(fromHash), true);
      triage.addEventListener("click", function (ev) {
        var row = ev.target.closest(".triage-row");
        if (!row) return;
        ev.preventDefault();
        select(row.dataset.rowFor, false);
      });
      window.addEventListener("hashchange", function () {
        var id = (location.hash.match(/^#f-(.+)$/) || [])[1];
        if (id && cardFor(decodeURIComponent(id))) select(decodeURIComponent(id), true, true);
      });
    }

    // Rows follow their finding into its state's group; triaging the
    // selected finding out of pending moves on to the next pending one.
    afterCounts = function () {
      var pendingBefore = selected && lastState[selected] === "pending";
      var orderBefore = visibleRows().map(function (r) { return r.dataset.rowFor; });
      cards.forEach(function (c) { lastState[c.dataset.finding] = c.dataset.state; });
      rows.forEach(function (row) {
        var card = cardFor(row.dataset.rowFor);
        var state = card ? card.dataset.state : "pending";
        row.className = row.className.replace(/\bstate-\S+/g, "").trim() + " state-" + state;
        var group = $('.triage-group[data-group="' + groupOf(state) + '"]', findingsPage);
        if (group && row.parentNode !== group) {
          var after = null;
          $$(".triage-row", group).some(function (r) {
            if (rows.indexOf(r) > rows.indexOf(row)) { after = r; return true; }
            return false;
          });
          group.insertBefore(row, after);
        }
      });
      $$(".triage-group", findingsPage).forEach(function (g) {
        var n = $$(".triage-row", g).length;
        g.hidden = n === 0;
        var count = $(".triage-group-head .count", g);
        if (count) count.textContent = n;
      });
      if (pendingBefore && lastState[selected] !== "pending") {
        var idx = orderBefore.indexOf(selected);
        var nextPending = orderBefore.slice(idx + 1).concat(orderBefore.slice(0, idx)).filter(function (id) {
          var r = rowFor(id);
          return r && r.classList.contains("state-pending") && !r.classList.contains("filtered");
        })[0];
        if (nextPending) select(nextPending, true);
      }
    };

    var filters = $("#finding-filters");
    if (filters && cards.length > 1) {
      var applyFilters = function () {
        var states = $$(".chip.active[data-filter-state]", filters).map(function (c) { return c.dataset.filterState; });
        var sevs = $$(".chip.active[data-filter-sev]", filters).map(function (c) { return c.dataset.filterSev; });
        var allChip = $("[data-filter-all]", filters);
        if (allChip) allChip.classList.toggle("active", states.length === 0);
        cards.forEach(function (card) {
          var stateOK = states.length === 0 || states.indexOf(card.dataset.state) >= 0 ||
            (states.indexOf("published") >= 0 && card.dataset.state === "note");
          var sevOK = sevs.length === 0 || sevs.indexOf(card.dataset.severity) >= 0;
          var hide = !(stateOK && sevOK);
          card.classList.toggle("filtered", hide);
          var row = rowFor(card.dataset.finding);
          if (row) row.classList.toggle("filtered", hide);
        });
        if (selected && rowFor(selected) && rowFor(selected).classList.contains("filtered")) {
          var vis = visibleRows()[0];
          if (vis) select(vis.dataset.rowFor, true);
        }
      };
      filters.addEventListener("click", function (ev) {
        var chip = ev.target.closest(".chip");
        if (!chip) return;
        if (chip.hasAttribute("data-filter-all")) {
          $$(".chip[data-filter-state]", filters).forEach(function (c) { c.classList.remove("active"); });
        } else {
          chip.classList.toggle("active");
        }
        applyFilters();
      });
    }

    var focusedCard = function () { return selected ? cardFor(selected) : null; };
    var move = function (delta) {
      var vis = visibleRows();
      if (!vis.length) return;
      var ids = vis.map(function (r) { return r.dataset.rowFor; });
      var idx = ids.indexOf(selected);
      var next = idx < 0 ? 0 : Math.max(0, Math.min(idx + delta, ids.length - 1));
      select(ids[next], true);
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
      ["a", "accept the selected finding"],
      ["x", "reject the selected finding"],
      ["e", "edit the selected finding's body"],
      ["A", "accept all pending findings"],
    ]);
    document.addEventListener("keydown", function (ev) {
      if (typing(ev) || ev.metaKey || ev.ctrlKey || ev.altKey) return;
      switch (ev.key) {
        case "j":
          move(1);
          break;
        case "k":
          move(-1);
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
        progress.textContent = n + " of " + files.length + " viewed";
        var bar = $("#viewed-bar");
        if (bar) bar.style.transform = "scaleX(" + n / files.length + ")";
      }
    };
    updateViewedUI();

    // Filter the explorer by path; folders left with no match fold away.
    var filterBox = $(".filetree-filter");
    var filterInput = $("#file-filter");
    if (filterBox && filterInput && files.length > 1) {
      filterBox.hidden = false;
      filterInput.addEventListener("input", function () {
        var q = filterInput.value.trim().toLowerCase();
        $$(".filetree li.file-entry").forEach(function (li) {
          li.hidden = q !== "" && li.dataset.path.toLowerCase().indexOf(q) < 0;
        });
        $$(".filetree details").forEach(function (d) {
          var any = $$("li.file-entry", d).some(function (li) { return !li.hidden; });
          d.parentNode.hidden = !any;
        });
      });
    }

    // Mark the file being read in the explorer.
    if ("IntersectionObserver" in window) {
      var entryFor = function (article) {
        return $('.filetree li.file-entry[data-path="' + CSS.escape(article.dataset.path) + '"] > a');
      };
      var io = new IntersectionObserver(function (entries) {
        entries.forEach(function (en) {
          if (!en.isIntersecting) return;
          $$(".filetree li.file-entry > a.current").forEach(function (a) { a.classList.remove("current"); });
          var link = entryFor(en.target);
          if (link) link.classList.add("current");
        });
      }, { rootMargin: "-120px 0px -60% 0px" });
      files.forEach(function (f) { io.observe(f); });
    }

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

    // Expandable diff context: each hunk header (and the file tail) can pull
    // in the unchanged lines around it, fetched from the new-side file at the
    // MR head. The server renders the rows; we splice them next to the
    // control and advance its boundary, dropping it once fully expanded.
    var CTX_STEP = 10;
    var ctxURL = diffLayout.dataset.ctxUrl;
    var ctxRef = diffLayout.dataset.ctxRef;
    var expanding = false;
    diffLayout.addEventListener("click", function (ev) {
      var btn = ev.target.closest(".expand-ctx");
      if (!btn || expanding || !ctxURL) return;
      var row = btn.closest("tr");
      var article = btn.closest(".diff-file");
      if (!row || !article) return;
      var down = row.dataset.ctxDown === "1";
      var offset = parseInt(row.dataset.ctxOffset, 10) || 0;
      var boundary = parseInt(row.dataset.ctxNew, 10);
      var minNew = parseInt(row.dataset.ctxMin, 10) || 1;
      var start, count;
      if (down) {
        start = boundary;
        count = CTX_STEP;
      } else {
        count = Math.min(CTX_STEP, boundary - minNew);
        if (count <= 0) { btn.remove(); return; }
        start = boundary - count;
      }
      var params = new URLSearchParams({
        path: article.dataset.path, ref: ctxRef,
        start: String(start), count: String(count), offset: String(offset),
      });
      expanding = true;
      btn.classList.add("loading");
      var done = function () { expanding = false; btn.classList.remove("loading"); };
      fetch(ctxURL + "&" + params.toString())
        .then(function (res) { if (!res.ok) throw new Error("context " + res.status); return res.text(); })
        .then(function (html) {
          var tbody = document.createElement("tbody");
          tbody.innerHTML = html.trim();
          var added = Array.prototype.slice.call(tbody.children);
          added.forEach(function (r) { row.parentNode.insertBefore(r, row); });
          if (down) {
            row.dataset.ctxNew = String(boundary + added.length);
            if (added.length < count) btn.remove();
          } else {
            row.dataset.ctxNew = String(start);
            if (start <= minNew) btn.remove();
          }
          done();
        })
        .catch(function () { toast("Could not load more lines", "error"); done(); });
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
        loadbarStart();
        open.querySelector("form").submit();
      }
    });
  }

  // --- MR list: lazy pipeline + thread hydration ------------------------
  // Pipeline status and unresolved-thread counts each cost a per-MR API
  // call, so the list renders without them and each row fetches its own
  // status JSON afterwards, a few at a time. Failures leave the cell empty.
  var statusCells = $$("td.mr-status[data-status-url]");
  if (statusCells.length) {
    // Same wording and glyphs as the server-rendered ciLabel/ciIcon.
    var ciIcons = { success: "check-circle", failed: "x-circle", running: "sync", canceled: "skip", canceling: "skip", skipped: "skip" };
    var ciLabel = function (status) {
      if (status === "success") return "Passed";
      if (status === "waiting_for_resource") return "Waiting";
      return status.charAt(0).toUpperCase() + status.slice(1).replace(/_/g, " ");
    };
    var renderStatus = function (cell, out) {
      cell.replaceChildren();
      if (out.pipeline && out.pipeline.status) {
        var p = out.pipeline;
        var badge = document.createElement(p.url ? "a" : "span");
        badge.className = "ci ci-" + p.status.replace(/[^a-z_-]/g, "");
        if (p.url) {
          badge.href = p.url;
          badge.target = "_blank";
          badge.rel = "noopener";
        }
        badge.title = "pipeline: " + p.status;
        badge.appendChild(statusIcon(ciIcons[p.status] || "clock"));
        badge.appendChild(document.createTextNode(" " + ciLabel(p.status)));
        cell.appendChild(badge);
      }
      var threadCell = cell.nextElementSibling;
      if (out.threads > 0 && threadCell) {
        var t = document.createElement("span");
        if (out.unresolved > 0) {
          t.className = "threads open";
          t.title = out.unresolved + " of " + out.threads + " thread(s) unresolved";
          t.appendChild(statusIcon("chat"));
          t.appendChild(document.createTextNode(" " + out.unresolved + " open"));
        } else {
          t.className = "threads";
          t.title = "all " + out.threads + " thread(s) resolved";
          t.appendChild(statusIcon("check"));
          t.appendChild(document.createTextNode(" " + out.threads + " resolved"));
        }
        threadCell.appendChild(t);
      }
    };
    var statusQueue = statusCells.slice();
    var pumpStatus = function () {
      var cell = statusQueue.shift();
      if (!cell) return;
      fetch(cell.dataset.statusUrl)
        .then(function (res) {
          if (!res.ok) throw new Error("HTTP " + res.status);
          return res.json();
        })
        .then(function (out) { renderStatus(cell, out); })
        .catch(function () { cell.replaceChildren(); })
        .then(pumpStatus, pumpStatus);
    };
    for (var s = 0; s < 4 && s < statusCells.length; s++) pumpStatus();
  }

  // --- settings: gitlab.instances add/remove ---------------------------
  // The server renders every instance as a fieldset plus one trailing blank
  // row. "Add instance" clones the blank row with a fresh index; "Remove"
  // deletes a row (an empty name is dropped server-side, so no-JS users
  // remove by clearing the name). Degrades to the single blank row without
  // this script.
  var instanceEditor = $("#instance-editor");
  if (instanceEditor) {
    var addInstanceBtn = $("#add-instance");
    var reindexRow = function (row, index) {
      $$("[name^='instance.']", row).forEach(function (field) {
        field.name = field.name.replace(/^instance\.\d+\./, "instance." + index + ".");
      });
      $$("[id^='inst-']", row).forEach(function (field) {
        var suffix = field.id.replace(/^inst-\d+-/, "");
        field.id = "inst-" + index + "-" + suffix;
      });
      $$("label[for^='inst-']", row).forEach(function (label) {
        var suffix = label.htmlFor.replace(/^inst-\d+-/, "");
        label.htmlFor = "inst-" + index + "-" + suffix;
      });
    };
    if (addInstanceBtn) {
      addInstanceBtn.addEventListener("click", function () {
        var rows = $$("[data-instance-row]", instanceEditor);
        var template = rows[rows.length - 1];
        if (!template) return;
        var index = parseInt(instanceEditor.dataset.nextIndex || "0", 10);
        instanceEditor.dataset.nextIndex = index + 1;
        var clone = template.cloneNode(true);
        reindexRow(clone, index);
        $$("input", clone).forEach(function (field) {
          if (field.type !== "hidden") field.value = "";
          if (field.name.indexOf(".orig_name") !== -1) field.value = "";
        });
        instanceEditor.appendChild(clone);
        var name = $("[name$='.name']", clone);
        if (name) name.focus();
      });
    }
    instanceEditor.addEventListener("click", function (ev) {
      var btn = ev.target.closest("[data-remove-instance]");
      if (!btn) return;
      var row = btn.closest("[data-instance-row]");
      if (row) row.remove();
    });
  }

  var settingsNav = $(".settings-nav");
  if (settingsNav && "IntersectionObserver" in window) {
    var markSection = new IntersectionObserver(function (entries) {
      entries.forEach(function (en) {
        if (!en.isIntersecting) return;
        $$("a", settingsNav).forEach(function (a) { a.classList.toggle("current", a.dataset.section === en.target.id); });
      });
    }, { rootMargin: "-80px 0px -70% 0px" });
    $$(".settings-section").forEach(function (sec) { markSection.observe(sec); });
  }

  // On narrow screens the file list and MR-level findings start folded so
  // the diff comes first.
  if (window.matchMedia && matchMedia("(max-width: 720px)").matches) {
    $$("details.card.general[open]").forEach(function (d) { d.open = false; });
  }
  var treeToggle = $(".filetree-toggle");
  if (treeToggle && window.matchMedia) {
    var tree = treeToggle.closest(".filetree");
    var narrow = window.matchMedia("(max-width: 1000px)");
    var syncTree = function () {
      var shown = !narrow.matches || tree.classList.contains("open");
      treeToggle.setAttribute("aria-expanded", shown ? "true" : "false");
    };
    treeToggle.addEventListener("click", function () {
      if (!narrow.matches) return;
      tree.classList.toggle("open");
      syncTree();
    });
    narrow.addEventListener("change", syncTree);
    syncTree();
  }

  // --- page-load indicator ----------------------------------------------
  // Every navigation is a full page load with no built-in feedback; show an
  // indeterminate bar at the top once one takes longer than a moment. This
  // submit listener registers after the triage one above, so upgraded forms
  // have already called preventDefault by the time it runs.
  var loadbar = $("#loadbar");
  var loadbarTimer = null;
  function loadbarStart() {
    if (!loadbar || loadbarTimer !== null) return;
    loadbarTimer = setTimeout(function () { loadbar.hidden = false; }, 150);
  }
  function loadbarStop() {
    if (loadbarTimer !== null) {
      clearTimeout(loadbarTimer);
      loadbarTimer = null;
    }
    if (loadbar) loadbar.hidden = true;
  }
  document.addEventListener("click", function (ev) {
    if (ev.defaultPrevented || ev.button !== 0 || ev.metaKey || ev.ctrlKey || ev.shiftKey || ev.altKey) return;
    var a = ev.target.closest && ev.target.closest("a[href]");
    if (!a || a.target || a.hasAttribute("download") || a.origin !== location.origin) return;
    var href = a.getAttribute("href");
    if (!href || href.charAt(0) === "#") return;
    loadbarStart();
  });
  document.addEventListener("submit", function (ev) {
    if (ev.defaultPrevented || ev.target.target || ev.target.method === "dialog") return;
    loadbarStart();
  });
  // A bfcache restore (back/forward) brings the old page back with the bar
  // up; Escape cancels an in-flight navigation.
  window.addEventListener("pageshow", loadbarStop);
  document.addEventListener("keydown", function (ev) {
    if (ev.key === "Escape") loadbarStop();
  });

  // Unhide the topbar help button on pages that registered shortcuts.
  var helpBtn = $("#help-toggle");
  if (helpBtn && shortcuts.length) {
    helpBtn.hidden = false;
    helpBtn.addEventListener("click", showHelp);
  }
})();
