// The Adapters page, under Settings: what Foyer connects to, by category —
// each adapter with the connections the household set up, every form drawn
// from the fields the adapter describes — each WebDAV share's folders made
// into libraries, and each portal's channels and the streams in use.
import { failed, send, settingsSidebar, when } from './common.js';

const masked = '******';

const categories = [
  ['sources', 'Sources', "Where the household's films, series and videos come from."],
  ['iptv', 'IPTV', "Live channels, and a provider's own films and series."],
  ['metadata', 'Metadata', 'What a title is: its name, dates, ids and artwork.'],
  ['tracking', 'Tracking', 'What each user watched, kept in step with a service of their own.'],
  ['tools', 'Tools', 'What Foyer runs on this server.'],
];

const icons = {
  webdav: 'ri-hard-drive-3-line',
  tmdb: 'ri-movie-2-line',
  ffmpeg: 'ri-cpu-line',
  jellyfin: 'ri-server-line',
  emby: 'ri-server-line',
  plex: 'ri-server-line',
  google_drive: 'ri-google-line',
  onedrive: 'ri-cloud-line',
  local: 'ri-folder-3-line',
  yattee: 'ri-video-line',
  invidious: 'ri-video-line',
  youtube: 'ri-youtube-line',
  stalker: 'ri-tv-2-line',
  m3u: 'ri-play-list-line',
  xtream: 'ri-tv-line',
  trakt: 'ri-history-line',
  simkl: 'ri-history-line',
  anilist: 'ri-history-line',
};

export function pageAdapters(route) {
  app.store.title = 'Adapters';
  const data = store({ loading: true });
  // Kept out of the store: whatever a reactive child reads while it builds
  // its elements, it is redrawn for, and a form redrawn as it is typed in
  // loses its focus.
  let views = [];

  async function load() {
    data.loading = true;
    try {
      views = await send('/api/foyer/admin/adapters');
    } catch (err) {
      failed(err);
    }
    data.loading = false;
  }

  // A connection opened by the address — ?connection=… or ?adapter=… — is
  // drawn open.
  const opened = (type, connection) =>
    (connection && route.query.connection?.[0] == connection.id) || route.query.adapter?.[0] == type.key;

  return t.div(
    {
      pbEvent: 'foyerAdapters',
      className: 'page page-foyer-adapters',
      onmount: load,
      onunmount: () => {
        app.store.errors = {};
      },
    },
    settingsSidebar(),
    t.div(
      { className: 'page-content full-height' },
      t.header(
        { className: 'page-header' },
        t.nav({ className: 'breadcrumbs' }, t.div({ className: 'breadcrumb-item' }, 'Settings'), t.div({ className: 'breadcrumb-item' }, () => app.store.title)),
      ),
      t.div({ className: 'wrapper m-b-base' }, () => {
        if (data.loading) {
          return t.div({ className: 'block txt-center' }, t.span({ className: 'loader lg' }));
        }
        return t.div(
          null,
          t.p(
            { className: 'txt-lg m-b-base' },
            "What Foyer connects to. A WebDAV share — Zurg's — lists the household's files, TMDB names them, and FFmpeg says what each one is. A share can be set up more than once.",
          ),
          t.div(
            { className: 'alert warning m-b-base' },
            'Passwords and keys are kept on this server as typed: anyone who signs in as a superuser, or holds a copy of pb_data or a backup, has them.',
          ),
          categories.map(([category, title, blurb]) => {
            const types = views.filter((v) => v.category == category);
            if (!types.length) {
              return null;
            }
            return t.div(
              { className: 'foyer-category m-b-lg' },
              t.h4({ className: 'm-b-5' }, title),
              t.p({ className: 'txt-hint m-b-sm' }, blurb),
              types.map((type) => typeBlock(type, load, opened)),
            );
          }),
        );
      }),
      t.footer({ className: 'page-footer' }, app.components.credits()),
    ),
  );
}

// typeBlock is one adapter: planned, set up once, or set up as many times as
// the household has shares.
function typeBlock(type, reload, opened) {
  if (type.planned) {
    return t.div(
      { className: 'foyer-planned m-b-sm' },
      t.i({ className: icons[type.key] || 'ri-plug-line', ariaHidden: true }),
      t.div(
        { className: 'flex-fill' },
        t.div(null, t.strong(null, type.name), ' ', t.span({ className: 'label sm' }, 'Planned')),
        t.div({ className: 'txt-hint txt-sm' }, type.description),
      ),
    );
  }
  if (type.single) {
    return connectionPanel(type, type.connections[0] || null, reload, opened(type, type.connections[0]));
  }
  const adding = store({ count: 0 });
  return t.div(
    { className: 'foyer-adapter m-b-sm' },
    t.div(
      { className: 'foyer-adapter-head' },
      t.i({ className: icons[type.key] || 'ri-plug-line', ariaHidden: true }),
      t.strong(null, type.name),
    ),
    t.p({ className: 'txt-hint txt-sm m-b-sm' }, type.description),
    type.connections.map((c) => connectionPanel(type, c, reload, opened(type, c))),
    () => (adding.count > 0 ? connectionPanel(type, null, reload, true, () => (adding.count = 0)) : null),
    t.button(
      { type: 'button', className: 'btn sm secondary', hidden: () => adding.count > 0, onclick: () => (adding.count = 1) },
      t.i({ className: 'ri-add-line', ariaHidden: true }),
      t.span({ className: 'txt' }, type.connections.length ? `Add another ${type.name} connection` : `Add a ${type.name} connection`),
    ),
  );
}

// connectionPanel is one connection's form — or a new one's, until it is
// saved. A single adapter's is saved by its key.
function connectionPanel(type, connection, reload, open, discard) {
  const key = type.key;
  const id = connection ? connection.id : `new_${key}`;
  const form = store({
    name: connection?.name || type.name,
    enabled: connection ? connection.enabled : true,
    values: {},
    status: null,
    init: 'null',
    saving: false,
    testing: false,
    result: null,
    // A new connection is a change already; a single adapter not yet saved
    // is not, until it is edited.
    get hasChanges() {
      return (!connection && !type.single) || form.init != JSON.stringify([form.name, form.enabled, form.values]);
    },
  });
  reset(connection);

  // reset reads nothing back from the store — only writes it: the panel is
  // built inside the page's reactive child, which would be redrawn for every
  // value read, and the form with it as it is typed in. A single adapter
  // starts off until it is saved; a new connection on.
  function reset(next) {
    const values = JSON.parse(JSON.stringify(next?.values || type.defaults || {}));
    const name = next?.name || type.name;
    const enabled = next ? next.enabled : !type.single;
    form.name = name;
    form.enabled = enabled;
    form.values = values;
    form.status = next?.status || null;
    form.init = JSON.stringify([name, enabled, values]);
  }

  async function save() {
    if (form.saving || !form.hasChanges) {
      return;
    }
    form.saving = true;
    const body = { name: form.name, enabled: form.enabled, values: app.utils.filterRedactedProps(form.values, masked) };
    try {
      let next;
      if (type.single) {
        next = await send(`/api/foyer/admin/adapters/${key}`, { method: 'PUT', body });
      } else if (connection) {
        next = await send(`/api/foyer/admin/connections/${connection.id}`, { method: 'PUT', body });
      } else {
        next = await send('/api/foyer/admin/connections', { method: 'POST', body: { ...body, adapter: key } });
      }
      app.toasts.success(`${next.name} saved.`);
      if (connection || type.single) {
        reset(next);
        if (!connection) {
          reload();
        }
      } else {
        // A new connection joins the list, where its folders can be listed.
        window.location.hash = `#/settings/adapters?connection=${next.id}`;
        reload();
      }
    } catch (err) {
      failed(err);
    }
    form.saving = false;
  }

  async function test() {
    form.testing = true;
    form.result = null;
    const path = connection ? `/api/foyer/admin/connections/${connection.id}/test` : `/api/foyer/admin/adapters/${key}/test`;
    try {
      form.result = await send(path, { method: 'POST', body: { values: app.utils.filterRedactedProps(form.values, masked) } });
    } catch (err) {
      failed(err);
    }
    form.testing = false;
  }

  function remove() {
    app.modals.confirm(
      t.div(
        null,
        t.p(null, `Remove ${connection.name}?`),
        t.p({ className: 'txt-hint' }, 'Its settings go. A share whose folders are libraries stays until they are removed.'),
      ),
      async () => {
        try {
          await send(`/api/foyer/admin/connections/${connection.id}`, { method: 'DELETE' });
          app.toasts.success(`${connection.name} removed.`);
          reload();
        } catch (err) {
          failed(err);
        }
      },
      null,
      { yesButton: 'Remove' },
    );
  }

  const shown = () => form.result || form.status;

  return t.details(
    { className: 'accordion m-b-sm', open: open || undefined },
    t.summary(
      null,
      t.i({ className: icons[key] || 'ri-plug-line', ariaHidden: true }),
      t.span({ className: 'txt' }, () => (type.single ? type.name : form.name || type.name)),
      connection || type.single ? null : t.span({ className: 'label sm' }, 'New'),
      t.div({ className: 'flex-fill' }),
      () => (form.enabled ? t.span({ className: 'label success' }, 'Enabled') : t.span({ className: 'label' }, 'Disabled')),
      () => {
        const s = form.status;
        if (s && !s.ok && form.enabled) {
          return t.i({ className: 'ri-error-warning-fill txt-danger', ariaDescription: app.attrs.tooltip(s.message, 'left') });
        }
      },
    ),
    type.single ? t.p({ className: 'txt-hint' }, type.description) : null,
    t.form(
      {
        className: 'grid',
        inert: () => form.saving,
        onsubmit: (e) => {
          e.preventDefault();
          save();
        },
      },
      type.single
        ? null
        : t.div(
            { className: 'col-lg-12' },
            t.div(
              { className: 'field' },
              t.label({ htmlFor: `${id}.name` }, 'Name'),
              t.input({
                id: `${id}.name`,
                name: 'name',
                type: 'text',
                autocomplete: 'off',
                required: true,
                value: () => form.name,
                oninput: (e) => (form.name = e.target.value),
              }),
            ),
            t.div({ className: 'field-help' }, 'What this connection is called here: "Zurg", "The NAS".'),
          ),
      t.div(
        { className: 'col-lg-12' },
        t.div(
          { className: 'field' },
          t.input({
            id: `${id}.enabled`,
            name: 'enabled',
            type: 'checkbox',
            className: 'switch',
            checked: () => form.enabled,
            onchange: (e) => (form.enabled = e.target.checked),
          }),
          t.label({ htmlFor: `${id}.enabled` }, 'Enabled'),
        ),
      ),
      type.fields.map((f) => fieldOf(form, id, f)),
      type.notice ? t.div({ className: 'col-lg-12' }, t.p({ className: 'txt-sm txt-hint' }, type.notice)) : null,
      t.div({ className: 'col-lg-12' }, () => {
        const s = shown();
        if (!s) {
          return null;
        }
        return t.div(
          { className: `alert ${s.ok ? 'success' : 'danger'}` },
          t.div(null, s.message, s.at && !form.result ? t.span({ className: 'txt-hint' }, ` — tested ${when(s.at)}`) : null),
          key == 'ffmpeg' && s.details ? detection(s.details) : null,
        );
      }),
      t.div(
        { className: 'col-lg-12' },
        t.div(
          { className: 'flex' },
          t.button(
            {
              type: 'button',
              className: () => `btn secondary ${form.testing ? 'loading' : ''}`,
              disabled: () => form.testing,
              onclick: test,
            },
            t.i({ className: 'ri-pulse-line', ariaHidden: true }),
            t.span({ className: 'txt' }, 'Test'),
          ),
          connection && !type.single
            ? t.button(
                { type: 'button', className: 'btn transparent danger', onclick: remove },
                t.i({ className: 'ri-delete-bin-7-line', ariaHidden: true }),
                t.span({ className: 'txt' }, 'Remove'),
              )
            : null,
          t.div({ className: 'm-r-auto' }),
          t.button(
            {
              type: 'button',
              className: 'btn transparent secondary',
              hidden: () => !form.hasChanges,
              onclick: () => (connection || type.single ? reset(connection) : discard?.()),
            },
            t.span({ className: 'txt' }, 'Cancel'),
          ),
          t.button(
            { className: () => `btn expanded-lg ${form.saving ? 'loading' : ''}`, disabled: () => !form.hasChanges || form.saving },
            t.span({ className: 'txt' }, () => (connection || type.single ? 'Save changes' : 'Add')),
          ),
        ),
      ),
    ),
    key == 'webdav' && connection ? libraries(connection, () => form.init) : null,
    key == 'stalker' && connection ? lineup(connection, () => form.init) : null,
  );
}

// lineup is a portal's channels as Foyer keeps them — how many, when they
// were read, why the last read failed — the lines in use, and a read now.
function lineup(connection, saved) {
  const data = store({ loading: false, reading: false, status: null });

  async function load() {
    data.loading = true;
    try {
      data.status = await send(`/api/foyer/admin/iptv/${connection.id}`);
    } catch (err) {
      failed(err);
    }
    data.loading = false;
  }

  async function readNow() {
    data.reading = true;
    try {
      data.status = await send(`/api/foyer/admin/iptv/${connection.id}/read`, { method: 'POST' });
      if (!data.status.error) {
        app.toasts.success(`${data.status.channels} channels read.`);
      }
    } catch (err) {
      failed(err);
    }
    data.reading = false;
  }

  let watcher;

  return t.div(
    {
      className: 'foyer-section m-t-base',
      onmount: () => {
        load();
        // A watcher runs once as it is made; only a later save reads again.
        watcher = watch(saved, (now, before) => {
          if (before !== undefined && now !== before) {
            load();
          }
        });
      },
      onunmount: () => watcher?.unwatch(),
    },
    t.header(
      null,
      t.h5(null, 'Channels'),
      app.components.refreshButton({ onclick: load }),
      t.div({ className: 'flex-fill' }),
      t.button(
        { type: 'button', className: () => `btn sm secondary ${data.reading ? 'loading' : ''}`, disabled: () => data.reading || !connection.enabled, onclick: readNow },
        t.i({ className: 'ri-refresh-line', ariaHidden: true }),
        t.span({ className: 'txt' }, 'Read the channels now'),
      ),
    ),
    t.p(
      { className: 'txt-hint' },
      "Foyer reads the portal's channels once in a while and keeps them, and every device of the household reads them from here: a portal counts requests.",
    ),
    () => {
      const s = data.status;
      if (!s) {
        return data.loading ? t.div({ className: 'block txt-center' }, t.span({ className: 'loader' })) : null;
      }
      return t.div(
        null,
        t.p(null, s.readAt ? `${s.channels} channels in ${s.groups} groups, read ${when(s.readAt)}.` : 'Not read yet: the first device that opens Live reads them.'),
        s.error ? t.div({ className: 'alert danger' }, s.error, s.triedAt ? t.span({ className: 'txt-hint' }, ` — ${when(s.triedAt)}`) : null) : null,
        t.h6({ className: 'm-t-sm' }, `Streams in use: ${s.lines.length} of ${s.streams}`),
        s.lines.length
          ? t.table(
              { className: 'foyer-table' },
              t.thead(null, t.tr(null, t.th(null, 'Channel'), t.th(null, 'Watching'))),
              t.tbody(
                null,
                s.lines.map((line) =>
                  t.tr(
                    null,
                    t.td(null, line.name, line.forced ? t.span({ className: 'label sm m-l-5' }, 'one more') : null),
                    t.td({ className: 'txt-sm' }, line.viewers.map((v) => (v.device ? `${v.user} on ${v.device}` : v.user)).join(', ')),
                  ),
                ),
              ),
            )
          : t.p({ className: 'txt-hint' }, 'None.'),
      );
    },
  );
}

// fieldOf draws one field an adapter asks for. Inputs are named as the
// server keys its errors, `values.<key>`, so PocketBase marks the right one;
// their ids are the connection's, as one adapter's fields can be on the page
// twice.
function fieldOf(form, connection, f) {
  const id = `${connection}.${f.key}`;
  const name = `values.${f.key}`;
  const help = f.help ? t.div({ className: 'field-help' }, f.help) : null;
  if (f.type == 'bool') {
    return t.div(
      { className: 'col-lg-12' },
      t.div(
        { className: 'field' },
        t.input({ id, name, type: 'checkbox', className: 'switch', checked: () => !!form.values[f.key], onchange: (e) => (form.values[f.key] = e.target.checked) }),
        t.label({ htmlFor: id }, f.label),
      ),
      help,
    );
  }
  let input;
  if (f.type == 'select') {
    input = app.components.select({
      id,
      name,
      required: !!f.required,
      options: f.options || [],
      value: () => form.values[f.key] ?? '',
      onchange: (selected) => (form.values[f.key] = selected?.[0]?.value),
    });
  } else if (f.type == 'number') {
    input = t.input({
      id,
      name,
      type: 'number',
      step: 1,
      min: f.min || undefined,
      max: f.max || undefined,
      required: !!f.required,
      value: () => form.values[f.key] ?? '',
      oninput: (e) => (form.values[f.key] = e.target.value === '' ? undefined : parseInt(e.target.value, 10)),
    });
  } else if (f.type == 'password' && f.visible) {
    // A secret that is no secret to whoever holds it — a portal's MAC
    // address — is typed in the clear, and read back to be edited.
    input = t.input({
      id,
      name,
      type: 'text',
      autocomplete: 'off',
      required: !!f.required,
      value: () => form.values[f.key] ?? '',
      oninput: (e) => (form.values[f.key] = e.target.value),
    });
  } else if (f.type == 'password') {
    // A secret that is set comes back masked: left alone it stays as it is,
    // and Backspace on it clears it.
    input = t.input({
      id,
      name,
      type: 'password',
      autocomplete: 'new-password',
      value: () => (form.values[f.key] === masked ? '' : form.values[f.key] || ''),
      placeholder: () => (form.values[f.key] === masked ? '* * * * * *' : ''),
      oninput: (e) => (form.values[f.key] = e.target.value),
      onkeyup: (e) => {
        if (e.key == 'Backspace' && form.values[f.key] === masked) {
          form.values[f.key] = '';
        }
      },
    });
  } else {
    input = t.input({
      id,
      name,
      type: f.type == 'url' ? 'url' : 'text',
      autocomplete: 'off',
      required: !!f.required,
      placeholder: f.default ?? '',
      value: () => form.values[f.key] ?? '',
      oninput: (e) => (form.values[f.key] = e.target.value),
    });
  }
  return t.div({ className: 'col-lg-6' }, t.div({ className: 'field' }, t.label({ htmlFor: id }, f.label), input), help);
}

// detection is what FFmpeg's test found: the tools, and the GPU tried.
function detection(d) {
  const encoders = (list) =>
    (list || []).map((e) =>
      t.span({ className: `label sm m-r-5 ${e.ok ? 'success' : 'danger'}`, title: e.error || '' }, e.codec),
    );
  const row = (label, ...value) => t.tr(null, t.th(null, label), t.td(null, ...value));
  return t.table(
    { className: 'foyer-table m-t-sm' },
    t.tbody(
      null,
      row('ffprobe', d.ffprobe || 'not found'),
      row('ffmpeg', d.ffmpeg || 'not found'),
      row('Acceleration', (d.hwaccels || []).join(', ') || 'none'),
      row('Render device', d.device || '', ' ', d.present ? t.span({ className: 'label sm success' }, 'present') : t.span({ className: 'label sm' }, 'absent')),
      d.vaapi?.length ? row('VAAPI', ...encoders(d.vaapi)) : null,
      d.qsv?.length ? row('Quick Sync', ...encoders(d.qsv)) : null,
      d.errors?.length ? row('Errors', t.div({ className: 'txt-sm' }, d.errors.join('\n'))) : null,
    ),
  );
}

// libraries is a share's folders, each a library or not yet, and the
// command that has Zurg tell Foyer of every change. saved changes as the
// share's settings are saved, so the folders are listed again.
function libraries(connection, saved) {
  const data = store({ loading: false, ok: false, message: '', folders: [], hook: null });

  async function load() {
    data.loading = true;
    try {
      const answer = await send(`/api/foyer/admin/connections/${connection.id}/folders`);
      data.ok = answer.ok;
      data.message = answer.message || '';
      data.folders = answer.folders || [];
    } catch (err) {
      failed(err);
    }
    data.loading = false;
  }

  async function loadHook() {
    try {
      data.hook = await send(`/api/foyer/admin/connections/${connection.id}/hook`);
    } catch (err) {
      failed(err);
    }
  }

  async function setEnabled(lib, enabled) {
    try {
      await send(`/api/foyer/admin/libraries/${lib.id}`, { method: 'PATCH', body: { enabled } });
      load();
    } catch (err) {
      failed(err);
    }
  }

  async function scan(lib) {
    try {
      await send('/api/foyer/admin/scan', { method: 'POST', body: { library: lib.id } });
      app.toasts.success(`Scanning ${lib.name}.`);
    } catch (err) {
      failed(err);
    }
  }

  function remove(lib) {
    const collections = lib.kind == 'shows' ? `${lib.name} and ${lib.name}_episodes` : lib.name;
    app.modals.confirm(
      t.div(
        null,
        t.p(null, `Remove the library ${lib.name}?`),
        t.p({ className: 'txt-hint' }, `Its collections — ${collections} — go, with their artwork. The share's files stay where they are, and what each user watched stays, ready for the library to come back.`),
      ),
      async () => {
        try {
          await send(`/api/foyer/admin/libraries/${lib.id}`, { method: 'DELETE' });
          app.toasts.success(`${lib.name} removed.`);
          load();
        } catch (err) {
          failed(err);
        }
      },
      null,
      { yesButton: 'Remove' },
    );
  }

  let watcher;

  return t.div(
    {
      className: 'foyer-section m-t-base',
      onmount: () => {
        load();
        loadHook();
        // A watcher runs once as it is made; only a later save lists again.
        watcher = watch(saved, (now, before) => {
          if (before !== undefined && now !== before) {
            load();
          }
        });
      },
      onunmount: () => watcher?.unwatch(),
    },
    t.header(
      null,
      t.h5(null, 'Libraries'),
      app.components.refreshButton({ onclick: load }),
      t.div({ className: 'flex-fill' }),
      t.button(
        { type: 'button', className: 'btn sm secondary', onclick: () => scan({ id: '', name: 'every library' }) },
        t.i({ className: 'ri-refresh-line', ariaHidden: true }),
        t.span({ className: 'txt' }, 'Scan every library'),
      ),
    ),
    t.p({ className: 'txt-hint' }, "Each of the share's folders can be a library: movies, or shows with their episodes. A folder that is not one — Zurg's __all__, a download folder — is never read."),
    () => (data.message ? t.div({ className: 'alert danger' }, data.message) : null),
    () => {
      if (data.loading && !data.folders.length) {
        return t.div({ className: 'block txt-center' }, t.span({ className: 'loader' }));
      }
      if (!data.folders.length) {
        return data.ok ? t.p({ className: 'txt-hint' }, 'The share lists no folders.') : null;
      }
      return t.table(
        { className: 'foyer-table' },
        t.thead(null, t.tr(null, t.th(null, 'Folder'), t.th(null, 'Library'), t.th(null, 'Last scan'), t.th(null, ''))),
        t.tbody(null, data.folders.map((f) => folderRow(f))),
      );
    },
    () =>
      data.hook
        ? t.div(
            { className: 'm-t-base' },
            t.h6(null, 'Scan as soon as the share changes'),
            t.p(
              { className: 'txt-hint txt-sm' },
              "Zurg runs on_library_update after every change: give it this in its config.yml, with the address Zurg reaches Foyer by in place of this page's own. Any other share's tools can post the folders that changed the same way.",
            ),
            t.div(
              { className: 'flex' },
              t.code({ className: 'foyer-command flex-fill' }, `on_library_update: |\n  ${data.hook.command}`),
              app.components.copyButton(() => `on_library_update: |\n  ${data.hook.command}`),
            ),
          )
        : null,
  );

  function folderRow(f) {
    const lib = f.library;
    if (!lib) {
      return t.tr(
        null,
        t.td({ className: 'path' }, f.folder),
        t.td({ className: 'txt-hint' }, 'Not a library'),
        t.td(null, ''),
        t.td(
          { className: 'txt-right' },
          t.button(
            { type: 'button', className: 'btn sm secondary', onclick: () => openNewLibrary(connection, f.folder, load) },
            t.i({ className: 'ri-add-line', ariaHidden: true }),
            t.span({ className: 'txt' }, 'Make a library'),
          ),
        ),
      );
    }
    const id = 'foyer_lib_' + lib.id;
    return t.tr(
      null,
      t.td({ className: 'path' }, f.folder),
      t.td(
        null,
        t.div(
          { className: 'field m-0' },
          t.input({ id, type: 'checkbox', className: 'switch', checked: lib.enabled, onchange: (e) => setEnabled(lib, e.target.checked) }),
          t.label({ htmlFor: id }, t.strong(null, lib.name), t.span({ className: 'txt-hint' }, ` · ${lib.kind}`)),
        ),
      ),
      t.td(
        { className: 'txt-sm' },
        lib.lastScanAt ? when(lib.lastScanAt) : t.span({ className: 'txt-hint' }, 'never'),
        lib.scanError ? t.div({ className: 'txt-danger' }, lib.scanError) : null,
      ),
      t.td(
        { className: 'txt-right txt-nowrap' },
        t.button({ type: 'button', className: 'btn sm transparent', disabled: !lib.enabled, onclick: () => scan(lib) }, t.span({ className: 'txt' }, 'Scan now')),
        t.a({ className: 'btn sm transparent', href: `#/library?library=${lib.id}` }, t.span({ className: 'txt' }, 'Open')),
        t.button(
          { type: 'button', className: 'btn sm transparent danger', onclick: () => remove(lib) },
          t.span({ className: 'txt' }, 'Remove'),
        ),
      ),
    );
  }
}

// A library's name is its collection's: lower case, digits and _, starting
// with a letter. The folder's own name is the guess.
function nameFor(folder) {
  let name = folder.toLowerCase().replace(/[^a-z0-9_]+/g, '_').replace(/^_+|_+$/g, '');
  if (!/^[a-z]/.test(name)) {
    name = 'lib_' + name;
  }
  return name.slice(0, 41);
}

function openNewLibrary(connection, folder, done) {
  const uniqueId = 'foyer_new_' + app.utils.randomString();
  const guessShows = /show|series|tv|anime/i.test(folder);
  const data = store({ name: nameFor(folder), kind: guessShows ? 'shows' : 'movies', enabled: true, saving: false });

  async function submit() {
    if (data.saving) {
      return;
    }
    data.saving = true;
    try {
      const lib = await send('/api/foyer/admin/libraries', {
        method: 'POST',
        body: { connection: connection.id, folder, name: data.name, kind: data.kind, enabled: data.enabled },
      });
      app.toasts.success(`${lib.name} made.`);
      app.modals.close();
      done();
      if (lib.enabled) {
        send('/api/foyer/admin/scan', { method: 'POST', body: { library: lib.id } }).catch(failed);
      }
    } catch (err) {
      failed(err);
    }
    data.saving = false;
  }

  const modal = t.div(
    { className: 'modal popup sm', onafterclose: (el) => el?.remove() },
    t.header({ className: 'modal-header' }, t.h5({ className: 'm-auto' }, `A library of ${folder}`)),
    t.form(
      {
        id: uniqueId,
        className: 'modal-content',
        onsubmit: (e) => {
          e.preventDefault();
          submit();
        },
      },
      t.div(
        { className: 'grid' },
        t.div(
          { className: 'col-lg-12' },
          t.div(
            { className: 'field' },
            t.label({ htmlFor: uniqueId + '.name' }, 'Name'),
            t.input({
              id: uniqueId + '.name',
              name: 'name',
              type: 'text',
              required: true,
              autocomplete: 'off',
              value: () => data.name,
              oninput: (e) => (data.name = e.target.value),
            }),
          ),
          t.div({ className: 'field-help' }, () =>
            data.kind == 'shows'
              ? `Its collections are ${data.name || '…'} and ${data.name || '…'}_episodes: lower case, digits and _, starting with a letter.`
              : `Its collection is ${data.name || '…'}: lower case, digits and _, starting with a letter.`,
          ),
        ),
        t.div(
          { className: 'col-lg-12' },
          t.div(
            { className: 'field' },
            t.label({ htmlFor: uniqueId + '.kind' }, 'It holds'),
            app.components.select({
              id: uniqueId + '.kind',
              name: 'kind',
              required: true,
              options: [
                { label: 'Movies', value: 'movies' },
                { label: 'Shows', value: 'shows' },
              ],
              value: () => data.kind,
              onchange: (selected) => (data.kind = selected?.[0]?.value || 'movies'),
            }),
          ),
          t.div({ className: 'field-help' }, 'Fixed once made.'),
        ),
        t.div(
          { className: 'col-lg-12' },
          t.div(
            { className: 'field' },
            t.input({ id: uniqueId + '.enabled', type: 'checkbox', className: 'switch', checked: () => data.enabled, onchange: (e) => (data.enabled = e.target.checked) }),
            t.label({ htmlFor: uniqueId + '.enabled' }, 'Scan it now, and with every scan'),
          ),
        ),
      ),
    ),
    t.footer(
      { className: 'modal-footer' },
      t.button({ type: 'button', className: 'btn transparent secondary m-r-auto', onclick: () => app.modals.close() }, t.span({ className: 'txt' }, 'Cancel')),
      t.button(
        { type: 'submit', 'html-form': uniqueId, className: () => `btn ${data.saving ? 'loading' : ''}`, disabled: () => data.saving },
        t.span({ className: 'txt' }, 'Make the library'),
      ),
    ),
  );
  document.body.appendChild(modal);
  app.modals.open(modal);
}
