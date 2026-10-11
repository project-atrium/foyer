// The Library page: every library's titles as posters, the newest added
// first — and a title's panel, where a guess is confirmed or corrected.
import { size } from './common.js';

const perPage = 48;

const states = {
  uncertain: { label: 'Guess', badge: 'warning', hint: 'Hidden from users until you confirm or correct it.' },
  unmatched: { label: 'No match', badge: 'danger', hint: 'TMDB had nothing by this name.' },
  manual: { label: 'Set by you', badge: 'info', hint: '' },
  matched: { label: 'Matched', badge: 'success', hint: '' },
};

const qualityLabels = { sd: 'SD', hd: 'HD', fhd: 'FHD', '2k': '2K', '4k': '4K', '8k': '8K' };

function send(path, options = {}) {
  return app.pb.send(path, { requestKey: null, ...options });
}

function failed(err) {
  if (!err?.isAbort) {
    app.checkApiError(err);
  }
}

function posterURL(item, thumb) {
  if (!item.poster) {
    return '';
  }
  return app.pb.files.getURL({ collectionId: item.collectionId, collectionName: item.collectionName, id: item.id }, item.poster, { thumb });
}

// added is the day a title's newest file came, as PocketBase writes dates.
function added(iso) {
  return iso.slice(0, 10);
}

export function pageLibrary(route) {
  app.store.title = 'Library';
  const requestKey = 'foyer_library_' + app.utils.randomString();
  const data = store({
    library: route.query.library?.[0] || '',
    state: route.query.state?.[0] || '',
    q: route.query.q?.[0] || '',
    libraries: [],
    items: [],
    counts: {},
    page: 0,
    total: 0,
    loading: false,
    scans: [],
    get more() {
      return data.items.length < data.total;
    },
  });
  let poll;
  let searchTimer;

  async function load(reset) {
    data.loading = true;
    const page = reset ? 1 : data.page + 1;
    try {
      const result = await app.pb.send('/api/foyer/admin/library', {
        requestKey,
        query: { page, perPage, library: data.library, state: data.state, q: data.q },
      });
      data.items = reset ? result.items : [...data.items, ...result.items];
      data.page = result.page;
      data.total = result.totalItems;
      data.counts = result.counts || {};
    } catch (err) {
      failed(err);
    }
    data.loading = false;
  }

  async function loadLibraries() {
    try {
      data.libraries = await app.pb.collection('libraries').getFullList({ sort: 'name', requestKey: null });
    } catch (err) {
      failed(err);
    }
  }

  async function status() {
    try {
      const result = await send('/api/foyer/admin/status');
      const wasRunning = data.scans.some((s) => s.running);
      data.scans = result.scans || [];
      if (wasRunning && !data.scans.some((s) => s.running)) {
        load(true);
      }
    } catch (err) {
      if (!err?.isAbort) {
        clearInterval(poll);
      }
    }
  }

  async function scanNow() {
    try {
      await send('/api/foyer/admin/scan', { method: 'POST', body: { library: data.library } });
      app.toasts.success('Scanning.');
      status();
    } catch (err) {
      failed(err);
    }
  }

  function choose(changes) {
    Object.assign(data, changes);
    app.utils.replaceHashQueryParams({ library: data.library || null, state: data.state || null, q: data.q || null });
    load(true);
  }

  function filterLink(label, changes, count, icon) {
    return t.a(
      {
        href: '#/library',
        className: () => {
          const active = Object.entries(changes).every(([k, v]) => data[k] == v);
          return `nav-item ${active ? 'active' : ''}`;
        },
        onclick: (e) => {
          e.preventDefault();
          choose(changes);
        },
      },
      t.i({ className: icon, ariaHidden: true }),
      t.span({ className: 'txt' }, label),
      () => (count() ? t.span({ className: 'label sm m-l-auto' }, String(count())) : null),
    );
  }

  return t.div(
    {
      pbEvent: 'foyerLibrary',
      className: 'page foyer-library',
      onmount: () => {
        loadLibraries();
        load(true);
        status();
        poll = setInterval(() => {
          if (!document.hidden) {
            status();
          }
        }, 2000);
      },
      onunmount: () => {
        clearInterval(poll);
        clearTimeout(searchTimer);
        app.pb.cancelRequest(requestKey);
      },
    },
    app.components.pageSidebar(
      { className: 'foyer-library-sidebar' },
      t.nav(
        { className: 'sidebar-content scrollable' },
        t.details(
          { className: 'nav-group', open: true },
          t.summary({ tabIndex: -1, onfocusout: () => false, onclick: () => false, onkeyup: () => false }, 'Titles'),
          filterLink('All', { state: '' }, () => data.counts.all, 'ri-film-line'),
          filterLink('Guesses', { state: 'uncertain' }, () => data.counts.uncertain, 'ri-question-line'),
          filterLink('No match', { state: 'unmatched' }, () => data.counts.unmatched, 'ri-error-warning-line'),
        ),
        t.details(
          { className: 'nav-group', open: true },
          t.summary({ tabIndex: -1, onfocusout: () => false, onclick: () => false, onkeyup: () => false }, 'Libraries'),
          filterLink('Every library', { library: '' }, () => 0, 'ri-stack-line'),
          () =>
            data.libraries.map((lib) =>
              filterLink(lib.name, { library: lib.id }, () => 0, lib.kind == 'shows' ? 'ri-tv-2-line' : 'ri-movie-2-line'),
            ),
        ),
      ),
    ),
    t.div(
      { className: 'page-content full-height' },
      t.header(
        { className: 'page-header' },
        t.nav({ className: 'breadcrumbs' }, t.div({ className: 'breadcrumb-item' }, 'Library')),
        app.components.refreshButton({ onclick: () => load(true) }),
        t.div(
          { className: 'flex gap-10 m-l-auto' },
          // PocketBase's own search bar edits filters; a title is plain text.
          t.form(
            {
              className: 'fields searchbar foyer-search',
              onsubmit: (e) => {
                e.preventDefault();
                clearTimeout(searchTimer);
                choose({ q: e.target.elements.q.value });
              },
            },
            t.div(
              { className: 'field' },
              t.input({
                type: 'search',
                name: 'q',
                className: 'p-l-20',
                placeholder: 'Find a title',
                value: () => data.q,
                oninput: (e) => {
                  clearTimeout(searchTimer);
                  const q = e.target.value;
                  searchTimer = setTimeout(() => choose({ q }), 300);
                },
              }),
            ),
          ),
          t.button(
            { type: 'button', className: 'btn', onclick: scanNow },
            t.i({ className: 'ri-refresh-line', ariaHidden: true }),
            t.span({ className: 'txt' }, 'Scan now'),
          ),
        ),
      ),
      t.div(
        // The page's whole width: PocketBase's wrapper holds a page to 840px.
        { className: 'foyer-full m-b-base' },
        () => {
          const running = data.scans.filter((s) => s.running);
          if (!running.length) {
            return null;
          }
          return t.div(
            { className: 'alert info foyer-banner' },
            running.map((s) =>
              t.div(
                null,
                t.strong(null, `Scanning ${s.name}`),
                ` — ${s.phase}`,
                s.entries ? `, ${s.walked} of ${s.entries} entries walked` : '',
                s.titles ? `, ${s.titles} titles placed` : '',
              ),
            ),
          );
        },
        () => {
          if (data.items.length) {
            return null;
          }
          if (data.loading) {
            return t.div({ className: 'block txt-center p-base' }, t.span({ className: 'loader lg' }));
          }
          return t.div(
            { className: 'block txt-center txt-hint p-base' },
            data.libraries.length
              ? 'Nothing here yet. A scan fills the library.'
              : t.span(null, 'No library yet: choose the share\'s folders under ', t.a({ href: '#/settings/adapters' }, 'Settings → Adapters'), '.'),
          );
        },
        t.div({ className: 'foyer-posters' }, () => data.items.map(posterCard)),
        t.div(
          { className: 'block txt-center m-t-base', hidden: () => !data.more },
          t.button(
            {
              type: 'button',
              className: () => `btn secondary ${data.loading ? 'loading' : ''}`,
              disabled: () => data.loading,
              onclick: () => load(false),
            },
            t.span({ className: 'txt' }, 'Load more'),
          ),
        ),
      ),
      t.footer({ className: 'page-footer' }, app.components.credits()),
    ),
  );

  function posterCard(item) {
    const state = states[item.matchState] || states.matched;
    const src = posterURL(item, '342x513');
    const best = (item.quality || []).slice(-1)[0];
    // No rid: shablon would keep the card it drew before, poster or none,
    // and drop the new one.
    return t.button(
      {
        type: 'button',
        className: 'foyer-poster',
        title: item.title,
        onclick: () => openTitle(item, () => load(true)),
      },
      t.div(
        { className: 'art' },
        src ? t.img({ src, alt: '', loading: 'lazy', draggable: false }) : t.i({ className: 'ri-film-line', ariaHidden: true }),
        t.div(
          { className: 'badges' },
          best ? t.span({ className: 'label sm' }, qualityLabels[best] || best) : null,
          item.matchState == 'uncertain' || item.matchState == 'unmatched'
            ? t.span({ className: `label sm ${state.badge}` }, state.label)
            : null,
        ),
      ),
      t.span({ className: 'title' }, item.title || 'Untitled'),
      t.small({ className: 'txt-hint' }, item.year ? String(item.year) : '\u00a0'),
      item.lastAdded ? t.small({ className: 'txt-hint' }, 'Added ' + added(item.lastAdded)) : null,
    );
  }
}

// openTitle shows a title's panel: what it is, its files, what TMDB answered,
// and the superuser's corrections — TMDB searched for the title it is, or its
// id typed.
function openTitle(item, changed) {
  const uniqueId = 'foyer_title_' + app.utils.randomString();
  const data = store({ detail: null, tmdbId: '', busy: false });
  const search = store({ query: item.title || '', year: item.year ? String(item.year) : '', results: null, searching: false });
  // Each candidate's poster, fetched with the superuser's token: a page's own <img> sends none.
  const posters = store({});
  const made = [];

  async function loadDetail() {
    try {
      data.detail = await send(`/api/foyer/admin/library/${item.collectionName}/${item.id}`);
    } catch (err) {
      failed(err);
    }
  }

  async function correct(path, body, done) {
    data.busy = true;
    try {
      await send(`/api/foyer/admin/library/${item.collectionName}/${item.id}/${path}`, { method: 'POST', body });
      app.toasts.success(done);
      app.modals.close();
      changed();
    } catch (err) {
      failed(err);
    }
    data.busy = false;
  }

  async function find(e, kind) {
    e.preventDefault();
    search.searching = true;
    try {
      const params = new URLSearchParams({ kind, q: search.query.trim() });
      if (/^\d{4}$/.test(search.year.trim())) {
        params.set('year', search.year.trim());
      }
      const answer = await send(`/api/foyer/admin/tmdb/search?${params}`);
      search.results = answer.results || [];
      for (const c of search.results) {
        if (c.poster && !posters[c.id]) {
          poster(c);
        }
      }
    } catch (err) {
      failed(err);
    }
    search.searching = false;
  }

  async function poster(c) {
    try {
      const res = await fetch(app.pb.buildURL(`/api/foyer/admin/tmdb/image/w92${c.poster}`), { headers: { Authorization: app.pb.authStore.token } });
      if (res.ok) {
        const url = URL.createObjectURL(await res.blob());
        made.push(url);
        posters[c.id] = url;
      }
    } catch {
      // A candidate without its picture is still one to pick.
    }
  }

  function searchSection(kind) {
    return t.div(
      { className: 'foyer-section m-t-base' },
      t.header(null, t.h5(null, 'Not this title?')),
      t.form(
        { className: 'flex gap-5 m-b-sm', onsubmit: (e) => find(e, kind) },
        t.div(
          { className: 'field m-0 flex-fill' },
          t.input({ name: 'query', type: 'text', required: true, placeholder: 'Search TMDB', value: () => search.query, oninput: (e) => (search.query = e.target.value) }),
        ),
        t.div(
          { className: 'field m-0', style: 'width: 90px' },
          t.input({ name: 'year', type: 'text', inputMode: 'numeric', placeholder: 'Year', value: () => search.year, oninput: (e) => (search.year = e.target.value) }),
        ),
        t.button(
          { type: 'submit', className: () => `btn secondary ${search.searching ? 'loading' : ''}`, disabled: () => search.searching || data.busy },
          t.span({ className: 'txt' }, 'Search'),
        ),
      ),
      () => {
        if (!search.results) {
          return t.p({ className: 'txt-hint txt-sm' }, 'TMDB, in its own order: pick what this title is, and its files are placed again.');
        }
        if (!search.results.length) {
          return t.p({ className: 'txt-hint' }, 'TMDB has nothing called so.');
        }
        return t.div(
          { className: 'foyer-candidates' },
          search.results.map((c) =>
            t.div(
              { className: 'foyer-candidate' },
              t.div({ className: 'art' }, () => (posters[c.id] ? t.img({ src: posters[c.id], alt: '' }) : t.i({ className: 'ri-image-line txt-hint', ariaHidden: true }))),
              t.div(
                { className: 'flex-fill' },
                t.strong(null, c.title, c.year ? ` (${c.year})` : ''),
                c.originalTitle ? t.div({ className: 'txt-hint txt-sm' }, c.originalTitle) : null,
                c.overview ? t.div({ className: 'txt-sm foyer-clamp' }, c.overview) : null,
                t.a({ className: 'txt-sm', href: `https://www.themoviedb.org/${kind}/${c.id}`, target: '_blank', rel: 'noopener noreferrer' }, `TMDB ${c.id}`),
              ),
              t.button(
                {
                  type: 'button',
                  className: () => `btn sm ${data.busy ? 'loading' : ''}`,
                  disabled: () => data.busy,
                  onclick: () => correct('match', { tmdbId: c.id }, `It is ${c.title}.`),
                },
                t.span({ className: 'txt' }, 'This one'),
              ),
            ),
          ),
        );
      },
    );
  }

  const modal = t.div(
    {
      className: 'modal popup lg',
      onafterclose: (el) => {
        made.forEach((url) => URL.revokeObjectURL(url));
        el?.remove();
      },
    },
    t.header({ className: 'modal-header' }, t.h5({ className: 'm-auto' }, () => data.detail?.item?.title || item.title)),
    t.div(
      { className: 'modal-content' },
      () => {
        const d = data.detail;
        if (!d) {
          return t.div({ className: 'block txt-center' }, t.span({ className: 'loader lg' }));
        }
        const r = d.item;
        const state = states[r.match_state] || states.matched;
        const src = posterURL(item, '342x513');
        const kind = d.library.kind == 'shows' ? 'tv' : 'movie';
        return t.div(
          null,
          t.div(
            { className: 'foyer-detail m-b-base' },
            t.div({ className: 'art' }, src ? t.img({ src, alt: '' }) : null),
            t.div(
              null,
              t.p(null, t.span({ className: `label ${state.badge}` }, state.label), ' ', t.span({ className: 'txt-hint' }, state.hint)),
              t.p(null, [r.year, d.library.name, r.content_rating].filter(Boolean).join(' · ')),
              r.overview ? t.p({ className: 'txt-hint' }, r.overview) : null,
              t.p(
                { className: 'txt-sm' },
                r.tmdb_id ? t.a({ href: `https://www.themoviedb.org/${kind}/${r.tmdb_id}`, target: '_blank', rel: 'noopener noreferrer' }, `TMDB ${r.tmdb_id}`) : 'No TMDB id',
                r.identity ? t.span({ className: 'txt-hint' }, ` · ${r.identity}`) : null,
              ),
              d.identity?.candidates?.length
                ? t.p(
                    { className: 'txt-sm txt-hint' },
                    'Also called so: ',
                    d.identity.candidates.map((c) => `${c.name || ''} ${c.year ? '(' + c.year + ')' : ''} — ${c.id}`).join('; '),
                  )
                : null,
            ),
          ),
          Object.keys(d.seasons || {}).length
            ? t.p({ className: 'txt-sm' }, 'Episodes held: ', Object.entries(d.seasons).map(([s, n]) => `season ${s}: ${n}`).join(', '))
            : null,
          t.table(
            { className: 'foyer-table' },
            t.thead(null, t.tr(null, t.th(null, 'File'), t.th(null, 'Episode'), t.th(null, 'What'), t.th(null, 'Size'), t.th(null, 'ffprobe'))),
            t.tbody(
              null,
              (d.files || []).map((f) =>
                t.tr(
                  null,
                  t.td(
                    { className: 'path', title: f.path },
                    f.path.split('/').pop(),
                    f.missing ? t.span({ className: 'label sm danger m-l-5' }, 'missing') : null,
                  ),
                  t.td(null, f.episode || (f.role != 'version' ? f.role : '')),
                  t.td(null, f.label || ''),
                  t.td(null, size(f.size)),
                  t.td(null, f.probeState || ''),
                ),
              ),
            ),
          ),
        );
      },
      searchSection(item.kind == 'shows' ? 'tv' : 'movie'),
    ),
    t.footer(
      { className: 'modal-footer' },
      t.a(
        { className: 'btn transparent m-r-auto', href: `#/collections?collection=${item.collectionName}&record=${item.id}`, onclick: () => app.modals.close() },
        t.span({ className: 'txt' }, 'Open record'),
      ),
      () =>
        data.detail?.item?.match_state == 'uncertain'
          ? t.button(
              {
                type: 'button',
                className: () => `btn secondary ${data.busy ? 'loading' : ''}`,
                disabled: () => data.busy,
                onclick: () => correct('confirm', {}, 'Confirmed.'),
              },
              t.span({ className: 'txt' }, 'Confirm'),
            )
          : null,
      t.form(
        {
          id: uniqueId,
          className: 'flex gap-5',
          onsubmit: (e) => {
            e.preventDefault();
            correct('match', { tmdbId: parseInt(data.tmdbId, 10) }, 'Set.');
          },
        },
        t.div(
          { className: 'field m-0' },
          t.input({
            name: 'tmdbId',
            type: 'number',
            min: 1,
            required: true,
            placeholder: 'TMDB id',
            value: () => data.tmdbId,
            oninput: (e) => (data.tmdbId = e.target.value),
          }),
        ),
        t.button(
          { type: 'submit', className: () => `btn ${data.busy ? 'loading' : ''}`, disabled: () => data.busy || !data.tmdbId },
          t.span({ className: 'txt' }, 'Set TMDB id'),
        ),
      ),
    ),
  );

  document.body.appendChild(modal);
  app.modals.open(modal);
  loadDetail();
}
