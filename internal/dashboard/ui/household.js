// The Household page, under Settings: what the household is called, who is
// in it, the devices they are signed in on, and a code that signs chosen
// users in on a new device — a television, where typing a password with a
// remote is a chore.
import { failed, send, settingsSidebar, when } from './common.js';

export function pageHousehold() {
  app.store.title = 'Household';
  const data = store({ loading: true, saving: false });
  const form = store({ name: '' });
  // Kept out of the store: whatever a reactive child reads while it builds
  // its elements, it is redrawn for.
  let view = { household: {}, members: [], devices: [] };

  async function load() {
    data.loading = true;
    try {
      view = await send('/api/foyer/admin/household');
      form.name = view.household.name || '';
    } catch (err) {
      failed(err);
    }
    data.loading = false;
  }

  async function rename(e) {
    e.preventDefault();
    if (data.saving) {
      return;
    }
    data.saving = true;
    try {
      const household = await send('/api/foyer/admin/household', { method: 'PATCH', body: { name: form.name } });
      view.household = household;
      app.toasts.success(`The household is ${household.name}.`);
    } catch (err) {
      failed(err);
    }
    data.saving = false;
  }

  function forget(device) {
    app.modals.confirm(
      t.div(
        null,
        t.p(null, `Forget ${device.name || 'this device'}?`),
        t.p(
          { className: 'txt-hint' },
          'It leaves this list. Its users stay signed in on it until it signs in again or their passwords change: Foyer ends a user’s sessions everywhere or nowhere.',
        ),
      ),
      async () => {
        try {
          await send(`/api/foyer/admin/devices/${device.id}`, { method: 'DELETE' });
          load();
        } catch (err) {
          failed(err);
        }
      },
      null,
      { yesButton: 'Forget' },
    );
  }

  return t.div(
    { pbEvent: 'foyerHousehold', className: 'page page-foyer-household', onmount: load },
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
            'One Foyer is one household, and its users are its profiles: each signs in on Loge as themselves, and one device can hold several. Users are made under Collections → users.',
          ),
          t.form(
            { className: 'grid m-b-base', onsubmit: rename },
            t.div(
              { className: 'col-lg-8' },
              t.div(
                { className: 'field' },
                t.label({ htmlFor: 'foyer_household_name' }, 'Name'),
                t.input({
                  id: 'foyer_household_name',
                  name: 'name',
                  type: 'text',
                  required: true,
                  maxLength: 50,
                  autocomplete: 'off',
                  value: () => form.name,
                  oninput: (e) => (form.name = e.target.value),
                }),
              ),
              t.div({ className: 'field-help' }, 'What Loge calls this Foyer.'),
            ),
            t.div(
              { className: 'col-lg-4' },
              t.button({ className: () => `btn expanded ${data.saving ? 'loading' : ''}`, disabled: () => data.saving }, t.span({ className: 'txt' }, 'Save')),
            ),
          ),
          membersSection(view.members),
          devicesSection(view.devices, forget),
        );
      }),
      t.footer({ className: 'page-footer' }, app.components.credits()),
    ),
  );
}

function membersSection(members) {
  return t.div(
    { className: 'foyer-section' },
    t.header(
      null,
      t.h5(null, 'Members'),
      t.div({ className: 'flex-fill' }),
      t.button(
        { type: 'button', className: 'btn sm', disabled: !members.length, onclick: () => openPairing(members) },
        t.i({ className: 'ri-qr-code-line', ariaHidden: true }),
        t.span({ className: 'txt' }, 'Pair a device'),
      ),
    ),
    members.length
      ? t.table(
          { className: 'foyer-table' },
          t.thead(null, t.tr(null, t.th(null, ''), t.th(null, 'Name'), t.th(null, 'Username'), t.th(null, 'PIN'), t.th(null, ''))),
          t.tbody(
            null,
            members.map((m) =>
              t.tr(
                null,
                t.td(null, m.avatar ? t.img({ className: 'foyer-avatar', src: m.avatar, alt: '' }) : t.i({ className: 'ri-user-line', ariaHidden: true })),
                t.td({ className: 'path' }, m.name),
                t.td(null, m.username),
                t.td(null, m.hasPin ? t.i({ className: 'ri-lock-line', ariaDescription: app.attrs.tooltip('A PIN locks this profile') }) : ''),
                t.td({ className: 'txt-right' }, t.a({ className: 'btn sm transparent', href: `#/collections?collection=users&record=${m.id}` }, t.span({ className: 'txt' }, 'Edit'))),
              ),
            ),
          ),
        )
      : t.p({ className: 'txt-hint' }, 'No user yet: make one for each person under Collections → users.'),
  );
}

function devicesSection(devices, forget) {
  return t.div(
    { className: 'foyer-section' },
    t.header(null, t.h5(null, 'Devices')),
    t.p({ className: 'txt-hint' }, 'Each device a user signed in on, or paired with a code, and when it was last seen.'),
    devices.length
      ? t.table(
          { className: 'foyer-table' },
          t.thead(null, t.tr(null, t.th(null, 'Device'), t.th(null, 'Users'), t.th(null, 'Last seen'), t.th(null, ''))),
          t.tbody(
            null,
            devices.map((d) =>
              t.tr(
                null,
                t.td(
                  { className: 'path' },
                  t.strong(null, d.name || 'A device'),
                  t.span({ className: 'txt-hint' }, [d.platform, d.appVersion].filter(Boolean).length ? ` · ${[d.platform, d.appVersion].filter(Boolean).join(' · ')}` : ''),
                  d.paired ? t.span({ className: 'label sm m-l-5' }, 'Paired') : null,
                ),
                t.td(null, (d.users || []).join(', ')),
                t.td({ className: 'txt-sm' }, when(d.lastSeen)),
                t.td(
                  { className: 'txt-right' },
                  t.button({ type: 'button', className: 'btn sm transparent danger', onclick: () => forget(d) }, t.span({ className: 'txt' }, 'Forget')),
                ),
              ),
            ),
          ),
        )
      : t.p({ className: 'txt-hint' }, 'No device yet.'),
  );
}

// openPairing makes a code for the users chosen: typed on the new device, it
// signs them in there, once, within ten minutes.
function openPairing(members) {
  const uniqueId = 'foyer_pair_' + app.utils.randomString();
  const data = store({ chosen: {}, making: false, code: '', expiresAt: '', left: '' });
  let timer;

  const tick = () => {
    const ms = new Date(data.expiresAt).getTime() - Date.now();
    if (ms <= 0) {
      data.left = 'expired';
      clearInterval(timer);
      return;
    }
    const s = Math.ceil(ms / 1000);
    data.left = `${Math.floor(s / 60)}:${String(s % 60).padStart(2, '0')}`;
  };

  async function make() {
    const users = members.filter((m) => data.chosen[m.id]).map((m) => m.id);
    if (!users.length || data.making) {
      return;
    }
    data.making = true;
    try {
      const answer = await send('/api/foyer/admin/pairings', { method: 'POST', body: { users } });
      data.code = answer.code;
      data.expiresAt = answer.expiresAt;
      tick();
      timer = setInterval(tick, 1000);
    } catch (err) {
      failed(err);
    }
    data.making = false;
  }

  const modal = t.div(
    {
      className: 'modal popup sm',
      onafterclose: (el) => {
        clearInterval(timer);
        el?.remove();
      },
    },
    t.header({ className: 'modal-header' }, t.h5({ className: 'm-auto' }, 'Pair a device')),
    t.div({ className: 'modal-content' }, () => {
      if (data.code) {
        return t.div(
          { className: 'txt-center' },
          t.p(null, 'On the device, choose Sign in to Foyer, then Use a pairing code, and type:'),
          t.div({ className: 'foyer-code' }, data.code),
          t.p({ className: 'txt-hint' }, () => (data.left == 'expired' ? 'This code has expired: make another.' : `It works once, for ${data.left} more.`)),
        );
      }
      return t.div(
        null,
        t.p({ className: 'txt-hint' }, 'Who should be signed in on the new device?'),
        members.map((m) =>
          t.div(
            { className: 'field' },
            t.input({
              id: `${uniqueId}.${m.id}`,
              type: 'checkbox',
              checked: () => !!data.chosen[m.id],
              onchange: (e) => (data.chosen = { ...data.chosen, [m.id]: e.target.checked }),
            }),
            t.label({ htmlFor: `${uniqueId}.${m.id}` }, m.name, t.span({ className: 'txt-hint' }, ` · ${m.username}`)),
          ),
        ),
      );
    }),
    t.footer(
      { className: 'modal-footer' },
      t.button({ type: 'button', className: 'btn transparent secondary m-r-auto', onclick: () => app.modals.close() }, t.span({ className: 'txt' }, () => (data.code ? 'Done' : 'Cancel'))),
      t.button(
        {
          type: 'button',
          className: () => `btn ${data.making ? 'loading' : ''}`,
          hidden: () => !!data.code,
          disabled: () => data.making || !members.some((m) => data.chosen[m.id]),
          onclick: make,
        },
        t.span({ className: 'txt' }, 'Make a code'),
      ),
    ),
  );
  document.body.appendChild(modal);
  app.modals.open(modal);
}
