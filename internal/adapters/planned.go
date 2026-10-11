package adapters

// Planned are the adapters Foyer will reach and cannot yet — every one Loge
// has, and those only a server can be — so the household sees on the
// Adapters page what is coming. Their fields are Loge's for the same service,
// so a connection set up later asks for what the app asks for now. An adapter
// that is built is registered before these and takes its place.
func Planned() []Adapter {
	server := func(label string) Field {
		return Field{Key: "serverUrl", Label: label, Type: URL, Required: true}
	}
	username := Field{Key: "username", Label: "Username", Type: Text, Required: true}
	password := Field{Key: "password", Label: "Password", Type: Password, Help: "Stored on this server as typed."}
	return []Adapter{
		{
			Key: "jellyfin", Category: Sources, Name: "Jellyfin", Planned: true,
			Description: "A Jellyfin server's films and series, and what it says each user watched.",
			Fields:      []Field{server("Server address"), username, password},
		},
		{
			Key: "emby", Category: Sources, Name: "Emby", Planned: true,
			Description: "An Emby server's films and series.",
			Fields:      []Field{server("Server address"), username, password},
		},
		{
			Key: "plex", Category: Sources, Name: "Plex", Planned: true,
			Description: "A Plex server's films and series.",
			Fields: []Field{
				server("Server address"),
				{Key: "token", Label: "Plex token", Type: Password, Required: true, Help: "Stored on this server as typed."},
			},
		},
		{
			Key: "google_drive", Category: Sources, Name: "Google Drive", Planned: true,
			Description: "Video files kept in Google Drive. It will sign in with Google.",
		},
		{
			Key: "onedrive", Category: Sources, Name: "OneDrive", Planned: true,
			Description: "Video files kept in OneDrive. It will sign in with Microsoft.",
		},
		{
			Key: "local", Category: Sources, Name: "Local folder", Planned: true,
			Description: "A folder on this server — a disk or a mount — read as a WebDAV share is.",
			Fields:      []Field{{Key: "path", Label: "Folder", Type: Text, Required: true, Help: "As this server sees it: inside the container, where Foyer runs in Docker."}},
		},
		{
			Key: "yattee", Category: Sources, Name: "Yattee Server", Planned: true,
			Description: "Web video through a Yattee Server.",
			Fields: []Field{
				server("Server address"), username,
				{Key: "password", Label: "Password", Type: Password, Required: true, Help: "Stored on this server as typed."},
			},
		},
		{
			Key: "invidious", Category: Sources, Name: "Invidious", Planned: true,
			Description: "Web video through an Invidious instance.",
			Fields:      []Field{{Key: "instanceUrl", Label: "Instance address", Type: URL, Required: true}},
		},
		{
			Key: "youtube", Category: Sources, Name: "YouTube", Planned: true, Single: true,
			Description: "YouTube itself, through yt-dlp: each user signed in with their own account, for their own recommendations.",
			Fields: []Field{
				{Key: "region", Label: "Region", Type: Text, Default: "US", Pattern: `^[A-Z]{2}$`},
				{Key: "language", Label: "Language", Type: Text, Default: "en", Pattern: `^[a-z]{2}(-[A-Z]{2})?$`},
			},
		},
		{
			Key: "stalker", Category: IPTV, Name: "Stalker portal", Planned: true,
			Description: "A Stalker or Ministra portal, reached as the one box its MAC address is: Foyer becomes the portal's only client, and shares its streams among the household's devices.",
			Fields: []Field{
				{Key: "portalUrl", Label: "Portal address", Type: URL, Required: true},
				{Key: "mac", Label: "MAC address", Type: Password, Visible: true, Required: true},
				{Key: "serialNumber", Label: "Serial number", Type: Password, Visible: true, Help: "Only if your provider asks for one."},
				{Key: "deviceId", Label: "Device id", Type: Password, Visible: true, Help: "Only if your provider asks for one."},
				{Key: "signature", Label: "Signature", Type: Password, Visible: true, Help: "Only if your provider asks for one."},
				{Key: "streams", Label: "Streams at once", Type: Number, Default: 1, Min: 1, Max: 4, Help: "As many as the subscription allows. The same channel on several devices is one."},
				{Key: "forced", Label: "Can be forced", Type: Bool, Help: "Lets a device open one more stream than allowed, for as long as it plays. The provider may end another stream for it."},
			},
		},
		{
			Key: "m3u", Category: IPTV, Name: "M3U playlist", Planned: true,
			Description: "Live channels from an M3U playlist, with an XMLTV guide.",
			Fields: []Field{
				{Key: "playlist", Label: "Playlist address", Type: Password, Required: true, Help: "It often holds the sign-in, so it is kept as a secret."},
				{Key: "guideUrl", Label: "Guide address", Type: URL, Help: "An XMLTV guide, if the playlist does not name one."},
			},
		},
		{
			Key: "xtream", Category: IPTV, Name: "Xtream Codes", Planned: true,
			Description: "Live channels, films and series from an Xtream Codes provider.",
			Fields: []Field{
				server("Server address"), username,
				{Key: "password", Label: "Password", Type: Password, Required: true, Help: "Stored on this server as typed."},
			},
		},
		{
			Key: "trakt", Category: Tracking, Name: "Trakt", Planned: true, Single: true,
			Description: "Each user's watch history kept in step with their own Trakt account, both ways.",
			Fields: []Field{
				{Key: "clientId", Label: "Client ID", Type: Text, Required: true, Help: "Of the household's own app, made at developer.trakt.tv."},
				{Key: "clientSecret", Label: "Client secret", Type: Password, Required: true, Help: "Stored on this server as typed."},
			},
		},
		{
			Key: "simkl", Category: Tracking, Name: "Simkl", Planned: true, Single: true,
			Description: "Each user's watch history kept in step with their own Simkl account.",
			Fields:      []Field{{Key: "clientId", Label: "Client ID", Type: Text, Required: true}},
		},
		{
			Key: "anilist", Category: Tracking, Name: "AniList", Planned: true, Single: true,
			Description: "Each user's anime kept in step with their own AniList account.",
			Fields: []Field{
				{Key: "clientId", Label: "Client ID", Type: Text, Required: true},
				{Key: "clientSecret", Label: "Client secret", Type: Password, Required: true, Help: "Stored on this server as typed."},
			},
		},
	}
}
