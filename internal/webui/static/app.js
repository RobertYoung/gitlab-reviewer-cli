// gitlab-reviewer GUI glue: SSE for review progress, inline comment forms
// on the diff. Everything else is plain forms and links.
(function () {
  "use strict";

  // --- review run page: stream progress, then jump to the findings ---
  const runPage = document.querySelector(".runpage");
  if (runPage && !runPage.dataset.done) {
    const log = document.getElementById("runlog");
    const es = new EventSource(runPage.dataset.events);
    // The stream replays the full history on every (re)connect; drop the
    // server-rendered snapshot so lines are not duplicated.
    es.onopen = () => {
      log.textContent = "";
    };
    es.addEventListener("line", (ev) => {
      log.textContent += JSON.parse(ev.data) + "\n";
      log.scrollTop = log.scrollHeight;
    });
    es.addEventListener("done", (ev) => {
      es.close();
      const out = JSON.parse(ev.data);
      if (!out.error && out.findingsUrl) {
        window.location.replace(out.findingsUrl);
      } else {
        // Cancelled or failed: reload so the server renders the outcome.
        window.location.reload();
      }
    });
    es.onerror = () => {
      // Server gone (ctrl+c) — stop retrying, leave the log on screen.
      if (es.readyState === EventSource.CLOSED) return;
    };
  }

  // --- chat page: stream the pending reply's progress, then re-render ---
  const chatPage = document.querySelector(".chatpage");
  if (chatPage) {
    if (!chatPage.dataset.idle) {
      const status = document.getElementById("chatstatus");
      const es = new EventSource(chatPage.dataset.events);
      // The stream replays the progress so far on every (re)connect; drop
      // the server-rendered snapshot so lines are not duplicated.
      es.onopen = () => {
        if (status) status.textContent = "";
      };
      es.addEventListener("line", (ev) => {
        if (!status) return;
        status.textContent += JSON.parse(ev.data) + "\n";
        status.scrollTop = status.scrollHeight;
      });
      es.addEventListener("done", () => {
        es.close();
        window.location.reload();
      });
      es.onerror = () => {
        // Server gone (ctrl+c) — stop retrying, leave the page on screen.
        if (es.readyState === EventSource.CLOSED) return;
      };
    }
    document.addEventListener("keydown", (ev) => {
      // ctrl/cmd+enter sends the message, like the TUI's ctrl+s.
      if ((ev.ctrlKey || ev.metaKey) && ev.key === "Enter") {
        const form = ev.target.closest("form.chat-form");
        if (form) form.submit();
      }
    });
  }

  // --- diff view: one floating comment form, moved to the clicked line ---
  const tmpl = document.getElementById("comment-form-template");
  if (tmpl) {
    let open = null; // the currently inserted <tr>

    const close = () => {
      if (open) {
        open.remove();
        open = null;
      }
    };

    document.addEventListener("click", (ev) => {
      const cancel = ev.target.closest(".cancel-comment");
      if (cancel) {
        close();
        return;
      }
      const btn = ev.target.closest(".add-comment");
      if (!btn) return;
      const row = btn.closest("tr.line");
      if (!row) return;
      close();

      const frag = tmpl.content.cloneNode(true);
      const tr = frag.querySelector("tr");
      const form = frag.querySelector("form");
      // Split-view buttons carry their own side's anchor; unified rows
      // carry it on the row.
      const src = btn.dataset.file !== undefined ? btn.dataset : row.dataset;
      form.querySelector('[name="file"]').value = src.file || "";
      form.querySelector('[name="old"]').value = src.old || "";
      form.querySelector('[name="new"]').value = src.new || "";
      // The form spans the code columns whatever the table layout.
      const tds = tr.querySelectorAll("td");
      tds[0].colSpan = row.cells.length === 6 ? 2 : 3;
      tds[1].colSpan = row.cells.length === 6 ? 4 : 1;
      row.after(tr);
      open = tr;
      tr.querySelector("textarea").focus();
    });

    document.addEventListener("keydown", (ev) => {
      if (ev.key === "Escape") close();
      // ctrl/cmd+enter submits the open comment form, like the TUI's ctrl+s.
      if ((ev.ctrlKey || ev.metaKey) && ev.key === "Enter" && open) {
        open.querySelector("form").submit();
      }
    });
  }
})();
