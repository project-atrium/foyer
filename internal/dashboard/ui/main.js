// Foyer on PocketBase's dashboard: its icon wherever the dashboard would show
// its own, and Foyer's pages — Library in the header, Adapters under
// Settings. PocketBase pastes this into /_/extensions.js and runs it as the
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
  app.routes.superuserOnly('#/settings/adapters', page('adapters.js', 'pageAdapters'));

  const links = app.store.headerLinks;
  const at = links.findIndex((link) => link.href === '#/settings');
  const library = { href: '#/library', icon: 'ri-film-line', label: 'Library' };
  app.store.headerLinks = at < 0 ? [...links, library] : [...links.slice(0, at), library, ...links.slice(at)];
  app.store.settingsNavGroups = {
    ...app.store.settingsNavGroups,
    Foyer: [{ href: '#/settings/adapters', icon: 'ri-plug-line', label: 'Adapters' }],
  };
}
