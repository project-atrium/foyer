// What Foyer's settings pages share: calling Foyer, saying what went wrong,
// dates as the dashboard writes them, and PocketBase's own settings sidebar,
// which it does not export — its groups, Foyer's first among them.

export function send(path, options = {}) {
  return app.pb.send(path, { requestKey: null, ...options });
}

export function failed(err) {
  if (!err?.isAbort) {
    app.checkApiError(err);
  }
}

export function when(iso) {
  return iso ? app.utils.toLocalDatetime(iso) : '';
}

// PocketBase's own settings sidebar, which it does not export: its groups,
// Foyer's among them.
export function settingsSidebar() {
  return app.components.pageSidebar(
    { className: 'settings-sidebar' },
    t.nav({ className: 'sidebar-content scrollable' }, () =>
      Object.entries(app.store.settingsNavGroups).map(([group, links]) =>
        t.details(
          { className: 'nav-group', 'html-data-group': group, open: true },
          t.summary({ tabIndex: -1, onfocusout: () => false, onclick: () => false, onkeyup: () => false }, group),
          links.map((link) => {
            const local = link.href.startsWith('#/');
            return t.a(
              {
                href: link.href,
                target: local ? undefined : '_blank',
                rel: local ? undefined : 'noopener noreferrer',
                className: (el) => `nav-item ${link.isActive?.(el) || app.utils.isActivePath(link.href, false) ? 'active' : ''}`,
              },
              link.icon ? t.i({ className: link.icon, ariaHidden: true }) : null,
              t.span({ className: 'txt' }, link.label),
            );
          }),
        ),
      ),
    ),
  );
}
