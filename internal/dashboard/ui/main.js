// Foyer on PocketBase's dashboard: its icon wherever the dashboard would show
// its own, and Foyer's pages — Library and Live Activity in the header,
// Adapters and Household under Settings. PocketBase pastes this into /_/extensions.js and runs it as the
// dashboard starts. Its router starts once this script has run, so the
// routes are registered at once, and each page's code loads when it is first
// opened. PocketBase calls these APIs experimental: the server's tests check
// its bundle still has every name used here.
if (window.app?.store) {
  const icon = './extensions/foyer/icon.png';
  app.store.favicon = icon;
  app.store.mainLogo = icon;
  app.store.headerLogo = icon;
}

if (window.app?.routes?.superuserOnly && !app.store._ready) {
  document.head.appendChild(t.link({ rel: 'stylesheet', href: './extensions/foyer/foyer.css' }));

  const page = (file, name) => async (route) => (await import(`./extensions/foyer/${file}`))[name](route);
  app.routes.superuserOnly('#/library', page('library.js', 'pageLibrary'));
  app.routes.superuserOnly('#/activity', page('activity.js', 'pageActivity'));
  app.routes.superuserOnly('#/settings/adapters', page('adapters.js', 'pageAdapters'));
  app.routes.superuserOnly('#/settings/household', page('household.js', 'pageHousehold'));

  // Settings opens on Foyer's own page, and stays lit on PocketBase's: the
  // link moves, PocketBase's #/settings route is left alone.
  const links = app.store.headerLinks.map((link) =>
    link.href === '#/settings'
      ? { ...link, href: '#/settings/adapters', isActive: () => window.location.hash.startsWith('#/settings') }
      : link,
  );
  const at = links.findIndex((link) => link.href === '#/settings/adapters');
  const ours = [
    { href: '#/library', icon: 'ri-film-line', label: 'Library' },
    { href: '#/activity', icon: 'ri-pulse-line', label: 'Live Activity' },
  ];
  app.store.headerLinks = at < 0 ? [...links, ...ours] : [...links.slice(0, at), ...ours, ...links.slice(at)];
  // Foyer's group first: the sidebar draws the groups in their keys' order.
  app.store.settingsNavGroups = {
    Foyer: [
      { href: '#/settings/adapters', icon: 'ri-plug-line', label: 'Adapters' },
      { href: '#/settings/household', icon: 'ri-home-heart-line', label: 'Household' },
    ],
    ...app.store.settingsNavGroups,
  };
}
