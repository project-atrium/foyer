// Live Activity: what plays now — who, on which device, what, how far, its
// bytes and their rate, and the player's engine — with Stop; the smaller
// copies being made for downloads, and those kept; and what played before.
// Read every two seconds while the page is open and shown, quietly: a
// server that did not answer is said once, not as a toast a poll.
import { failed, send, size, when } from './common.js';

const perPage = 30;

// What a poll changes of a playing; the rest stays as it first came.
const changing = ['device', 'client', 'mode', 'speed', 'lastSeen', 'quiet', 'positionMs', 'durationMs', 'paused', 'bytes', 'bitrate', 'streams'];

const modes = { direct: 'Direct', remux: 'Remuxed', transcode: 'Transcoded', copy: 'Smaller copy', relay: 'Relayed' };

// What a poll changes of a copy being made.
const makingNow = ['state', 'progress', 'speed', 'etaMs', 'error', 'started'];

const names = { av1: 'AV1', hevc: 'HEVC', h264: 'H.264', vp9: 'VP9', opus: 'Opus', aac: 'AAC', mkv: 'Matroska', mp4: 'MP4' };
const preferences = { size: 'smaller file', balanced: 'balanced', quality: 'better picture' };

// madeAs is what a copy is made to, in a line: AV1 · 1080p · balanced · Opus · Matroska.
function madeAs(e = {}) {
  return [names[e.video] || e.video, e.maxHeight ? `${e.maxHeight}p` : null, preferences[e.preference], names[e.audio] || e.audio, names[e.container] || e.container, e.hdr ? 'HDR' : null]
    .filter(Boolean)
    .join(' · ');
}

// minutes is a time left as people say it: 25 min, 1 h 5 min.
function minutes(ms) {
  const m = Math.max(1, Math.round(ms / 60000));
  return m >= 60 ? `${Math.floor(m / 60)} h ${m % 60} min` : `${m} min`;
}

// clock is a position as a player shows it: 1:02:03, or 2:03.
function clock(ms) {
  const s = Math.max(0, Math.floor(ms / 1000));
  const h = Math.floor(s / 3600);
  const m = Math.floor((s % 3600) / 60);
  const rest = String(s % 60).padStart(2, '0');
  return h ? `${h}:${String(m).padStart(2, '0')}:${rest}` : `${m}:${rest}`;
}

function rate(bits) {
  if (!bits) {
    return '';
  }
  return bits >= 1e6 ? `${(bits / 1e6).toFixed(1)} Mb/s` : `${Math.max(1, Math.round(bits / 1e3))} kb/s`;
}

function deviceLine(device) {
  const parts = [device?.name || 'A device', device?.platform, device?.appVersion].filter(Boolean);
  return parts.join(' · ');
}

// howFar is where a playing got to, or what of a download came.
function howFar(p) {
  if (p.kind == 'download') {
    return size(p.bytes) || '0 B';
  }
  if (!p.durationMs) {
    return p.positionMs ? clock(p.positionMs) : '';
  }
  return `${clock(p.positionMs)} of ${clock(p.durationMs)}`;
}

export function pageActivity() {
  app.store.title = 'Live Activity';
  const data = store({ ids: [], making: [], kept: [], loading: true, unreachable: false, history: [], historyPage: 0, historyMore: false, historyLoading: false });
  // Each playing as it first came, and a store of what changes, which a poll
  // writes in place: the list is drawn again only when a playing comes or
  // goes, and a poster is never fetched twice.
  const playing = {};
  // The copies being made likewise; those kept are drawn again only when
  // one comes, goes or is asked for.
  const copies = {};
  let keptSaid = '';
  let poll;

  async function refresh() {
    try {
      const answer = await send('/api/foyer/admin/sessions');
      const ids = (answer.sessions || []).map((s) => s.id);
      for (const view of answer.sessions || []) {
        const known = playing[view.id];
        if (!known) {
          playing[view.id] = { first: view, now: store(Object.fromEntries(changing.map((k) => [k, view[k]]))) };
          continue;
        }
        for (const k of changing) {
          if (JSON.stringify(known.now[k]) !== JSON.stringify(view[k])) {
            known.now[k] = view[k];
          }
        }
      }
      const ended = Object.keys(playing).filter((id) => !ids.includes(id));
      for (const id of ended) {
        delete playing[id];
      }
      if (ids.join() !== data.ids.join()) {
        data.ids = ids;
      }
      if (ended.length) {
        loadHistory(true);
      }
      const made = await send('/api/foyer/admin/copies');
      const keys = (made.making || []).map((c) => c.key);
      for (const view of made.making || []) {
        const known = copies[view.key];
        if (!known) {
          copies[view.key] = { first: view, now: store(Object.fromEntries(makingNow.map((k) => [k, view[k]]))) };
          continue;
        }
        for (const k of makingNow) {
          if (JSON.stringify(known.now[k]) !== JSON.stringify(view[k])) {
            known.now[k] = view[k];
          }
        }
      }
      for (const key of Object.keys(copies).filter((key) => !keys.includes(key))) {
        delete copies[key];
      }
      if (keys.join() !== data.making.join()) {
        data.making = keys;
      }
      const said = JSON.stringify((made.kept || []).map((k) => [k.key, k.asked]));
      if (said !== keptSaid) {
        keptSaid = said;
        data.kept = made.kept || [];
      }
      data.unreachable = false;
    } catch (err) {
      if (err?.isAbort) {
        return;
      }
      if (err?.status == 401 || err?.status == 403) {
        clearInterval(poll);
        failed(err);
        return;
      }
      data.unreachable = true;
    }
    data.loading = false;
  }

  async function loadHistory(reset) {
    data.historyLoading = true;
    const page = reset ? 1 : data.historyPage + 1;
    try {
      const answer = await send('/api/foyer/admin/sessions/history', { query: { page, perPage } });
      data.history = reset ? answer.items : [...data.history, ...answer.items];
      data.historyPage = page;
      data.historyMore = answer.items.length == perPage;
    } catch (err) {
      failed(err);
    }
    data.historyLoading = false;
  }

  function stop(p) {
    const what = [p.item.title, p.item.subtitle].filter(Boolean).join(' — ') || 'this';
    app.modals.confirm(
      t.div(
        null,
        t.p(null, `Stop ${p.user.name || 'this user'}’s ${p.kind == 'download' ? 'download' : 'playing'} of ${what}?`),
        t.p({ className: 'txt-hint' }, 'Its stream ends now, and the address it holds is refused from now on. Playing it again asks Foyer afresh.'),
      ),
      async () => {
        try {
          await send(`/api/foyer/admin/sessions/${p.id}/stop`, { method: 'POST' });
        } catch (err) {
          failed(err);
        }
        refresh();
      },
      null,
      { yesButton: 'Stop' },
    );
  }

  function stopCopy(c) {
    app.modals.confirm(
      t.div(
        null,
        t.p(null, `Stop making the copy of ${c.title || 'this'}?`),
        t.p({ className: 'txt-hint' }, 'Nothing of it is kept. A device that still wants it asks again, and it is made again.'),
      ),
      async () => {
        try {
          await send(`/api/foyer/admin/copies/${c.key}/stop`, { method: 'POST' });
        } catch (err) {
          failed(err);
        }
        refresh();
      },
      null,
      { yesButton: 'Stop' },
    );
  }

  function removeCopy(k) {
    app.modals.confirm(
      t.div(
        null,
        t.p(null, `Let go of the copy of ${k.title || 'this'}?`),
        t.p({ className: 'txt-hint' }, 'A device that kept it keeps it. Asked for again, it is made again.'),
      ),
      async () => {
        try {
          await send(`/api/foyer/admin/copies/${k.key}`, { method: 'DELETE' });
        } catch (err) {
          failed(err);
        }
        refresh();
      },
      null,
      { yesButton: 'Let go' },
    );
  }

  return t.div(
    {
      pbEvent: 'foyerActivity',
      className: 'page foyer-activity',
      onmount: () => {
        refresh();
        loadHistory(true);
        poll = setInterval(() => {
          if (!document.hidden) {
            refresh();
          }
        }, 2000);
      },
      onunmount: () => clearInterval(poll),
    },
    t.div(
      { className: 'page-content full-height' },
      t.header(
        { className: 'page-header' },
        t.nav({ className: 'breadcrumbs' }, t.div({ className: 'breadcrumb-item' }, 'Live Activity')),
        app.components.refreshButton({
          onclick: () => {
            refresh();
            loadHistory(true);
          },
        }),
        t.div({ className: 'm-l-auto txt-hint txt-sm' }, () => (data.unreachable ? 'Foyer did not answer; trying again.' : '')),
      ),
      t.div(
        { className: 'foyer-full m-b-base' },
        t.div(
          { className: 'foyer-section' },
          t.header(null, t.h5(null, 'Playing now'), () => (data.ids.length ? t.span({ className: 'label sm' }, String(data.ids.length)) : null)),
          () => {
            if (data.loading) {
              return t.div({ className: 'block txt-center' }, t.span({ className: 'loader lg' }));
            }
            if (!data.ids.length) {
              return t.p({ className: 'txt-hint' }, 'Nothing plays now.');
            }
            return t.div(
              { className: 'foyer-playings' },
              data.ids.map((id) => playingCard(playing[id].first, playing[id].now, stop)),
            );
          },
        ),
        t.div(
          { className: 'foyer-section' },
          t.header(null, t.h5(null, 'Copies for downloads'), () => (data.making.length ? t.span({ className: 'label sm' }, String(data.making.length)) : null)),
          () => {
            if (data.loading) {
              return null;
            }
            if (!data.making.length) {
              return t.p({ className: 'txt-hint' }, data.kept.length ? 'No copy is being made.' : 'No copy is being made, and none is kept. A device keeping a film asks for one, made to its own settings.');
            }
            return t.div(
              { className: 'foyer-playings' },
              data.making.map((key) => copyCard(copies[key].first, copies[key].now, stopCopy)),
            );
          },
          () => keptTable(data.kept, removeCopy),
        ),
        t.div(
          { className: 'foyer-section' },
          t.header(null, t.h5(null, 'History')),
          () => historyTable(data.history),
          t.div(
            { className: 'block txt-center m-t-sm', hidden: () => !data.historyMore },
            t.button(
              {
                type: 'button',
                className: () => `btn sm secondary ${data.historyLoading ? 'loading' : ''}`,
                disabled: () => data.historyLoading,
                onclick: () => loadHistory(false),
              },
              t.span({ className: 'txt' }, 'Load more'),
            ),
          ),
        ),
      ),
      t.footer({ className: 'page-footer' }, app.components.credits()),
    ),
  );
}

// playingCard draws a playing from what never changes of it, and reads what
// does only inside functions: read while it is built, the list would be
// drawn again at every poll.
function playingCard(first, now, stop) {
  const item = first.item || {};
  return t.div(
    { className: 'foyer-playing' },
    t.div(
      { className: 'art' },
      item.poster
        ? t.img({ src: item.poster, alt: '', draggable: false })
        : t.i({ className: first.kind == 'download' ? 'ri-download-2-line' : 'ri-film-line', ariaHidden: true }),
    ),
    t.div(
      { className: 'about' },
      t.div({ className: 'title' }, item.title || 'Something no longer in the library'),
      item.subtitle ? t.div({ className: 'txt-hint txt-sm' }, item.subtitle) : null,
      t.div(
        { className: 'who txt-sm' },
        t.i({ className: 'ri-user-line', ariaHidden: true }),
        t.span(null, first.user?.name || 'A user'),
        t.i({ className: 'ri-device-line m-l-5', ariaHidden: true }),
        t.span(null, () => deviceLine(now.device)),
      ),
      () =>
        first.kind == 'download' || !now.durationMs
          ? null
          : t.div({ className: 'foyer-progress' }, t.div({ style: `width: ${Math.min(100, (100 * now.positionMs) / now.durationMs).toFixed(1)}%` })),
      t.div({ className: 'meta txt-sm' }, () => {
        const labels = [];
        labels.push(t.span({ className: 'label sm' }, first.kind == 'download' ? 'Download' : modes[now.mode] || now.mode));
        if (first.kind == 'download' && now.mode == 'copy') {
          labels.push(t.span({ className: 'label sm' }, modes.copy));
        }
        if (now.paused) {
          labels.push(t.span({ className: 'label sm warning' }, 'Paused'));
        }
        if (now.quiet) {
          labels.push(t.span({ className: 'label sm', ariaDescription: app.attrs.tooltip('Nothing heard from it for a while: a phone asleep, or a player closed without a word') }, `Quiet since ${when(now.lastSeen)}`));
        }
        // A remux's speed is ffmpeg's: how much faster than it plays it is made.
        const speed = now.mode == 'remux' && now.speed ? `made at ${now.speed.toFixed(1)}×` : '';
        const facts = [howFar({ kind: first.kind, ...now }), rate(now.bitrate), speed, now.client].filter(Boolean);
        return t.div({ className: 'flex gap-5 flex-wrap' }, labels, t.span({ className: 'txt-hint' }, facts.join(' · ')));
      }),
    ),
    t.div(
      { className: 'actions' },
      t.button(
        { type: 'button', className: 'btn sm transparent danger', onclick: () => stop({ ...first, ...now }) },
        t.i({ className: 'ri-stop-circle-line', ariaHidden: true }),
        t.span({ className: 'txt' }, 'Stop'),
      ),
    ),
  );
}

function historyTable(items) {
  if (!items.length) {
    return t.p({ className: 'txt-hint' }, 'Nothing has played yet.');
  }
  return t.table(
    { className: 'foyer-table' },
    t.thead(null, t.tr(null, t.th(null, 'Ended'), t.th(null, 'What'), t.th(null, 'Who'), t.th(null, 'Where'), t.th(null, 'How far'), t.th(null, ''))),
    t.tbody(
      null,
      items.map((h) =>
        t.tr(
          null,
          t.td({ className: 'txt-sm' }, when(h.ended)),
          t.td(
            { className: 'foyer-what' },
            t.strong(null, h.item?.title || '—'),
            h.item?.subtitle ? t.span({ className: 'txt-hint' }, ` · ${h.item.subtitle}`) : null,
          ),
          t.td(null, h.user?.name || 'A user no longer here'),
          t.td({ className: 'txt-sm' }, deviceLine(h.device), h.client ? t.div({ className: 'txt-hint' }, h.client) : null),
          t.td({ className: 'txt-sm' }, howFar(h)),
          t.td(
            { className: 'txt-right' },
            h.kind == 'download' ? t.span({ className: 'label sm' }, 'Download') : null,
            h.mode == 'copy' ? t.span({ className: 'label sm m-l-5' }, modes.copy) : null,
            h.revoked ? t.span({ className: 'label sm danger m-l-5' }, 'Stopped') : null,
          ),
        ),
      ),
    ),
  );
}

// copyCard draws a copy being made from what never changes of it, and reads
// what a poll changes only inside functions, as a playing's card does.
function copyCard(first, now, stop) {
  return t.div(
    { className: 'foyer-playing' },
    t.div({ className: 'art' }, t.i({ className: 'ri-file-reduce-line', ariaHidden: true })),
    t.div(
      { className: 'about' },
      t.div({ className: 'title' }, first.title || 'A file no longer in the library'),
      t.div({ className: 'txt-hint txt-sm' }, [madeAs(first.encode), first.estimatedBytes ? `about ${size(first.estimatedBytes)}` : ''].filter(Boolean).join(' · ')),
      first.by ? t.div({ className: 'who txt-sm' }, t.i({ className: 'ri-user-line', ariaHidden: true }), t.span(null, first.by)) : null,
      () => (now.state == 'running' ? t.div({ className: 'foyer-progress' }, t.div({ style: `width: ${Math.min(100, 100 * now.progress).toFixed(1)}%` })) : null),
      t.div({ className: 'meta txt-sm' }, () => {
        const label = { queued: 'Waiting', running: 'Being made', failed: 'Could not be made' }[now.state] || now.state;
        // How fast is ffmpeg's: how much faster than the film runs it is made.
        const facts =
          now.state == 'running'
            ? [`${Math.floor(100 * now.progress)} %`, now.speed ? `made at ${now.speed.toFixed(1)}×` : '', now.etaMs ? `${minutes(now.etaMs)} left` : '']
            : [now.state == 'failed' ? now.error : ''];
        return t.div(
          { className: 'flex gap-5 flex-wrap' },
          t.span({ className: `label sm ${now.state == 'failed' ? 'danger' : ''}` }, label),
          t.span({ className: 'txt-hint' }, facts.filter(Boolean).join(' · ')),
        );
      }),
    ),
    t.div(
      { className: 'actions' },
      t.button(
        { type: 'button', className: 'btn sm transparent danger', onclick: () => stop({ ...first, ...now }) },
        t.i({ className: 'ri-stop-circle-line', ariaHidden: true }),
        t.span({ className: 'txt' }, 'Stop'),
      ),
    ),
  );
}

// keptTable is the copies kept, the latest asked for first.
function keptTable(items, remove) {
  if (!items.length) {
    return null;
  }
  return t.table(
    { className: 'foyer-table m-t-sm' },
    t.thead(null, t.tr(null, t.th(null, 'Kept'), t.th(null, 'Made as'), t.th(null, 'Size'), t.th(null, 'Last asked for'), t.th(null, ''))),
    t.tbody(
      null,
      items.map((k) =>
        t.tr(
          null,
          t.td({ className: 'foyer-what' }, t.strong(null, k.title || '—'), k.by ? t.span({ className: 'txt-hint' }, ` · ${k.by}`) : null),
          t.td({ className: 'txt-sm' }, madeAs(k.encode)),
          t.td({ className: 'txt-sm' }, size(k.size)),
          t.td({ className: 'txt-sm' }, when(k.asked)),
          t.td(
            { className: 'txt-right' },
            t.button(
              { type: 'button', className: 'btn sm transparent', ariaLabel: `Let go of the copy of ${k.title || 'this'}`, onclick: () => remove(k) },
              t.i({ className: 'ri-delete-bin-line', ariaHidden: true }),
            ),
          ),
        ),
      ),
    ),
  );
}
