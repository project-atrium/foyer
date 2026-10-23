package iptv

//go:generate go run gen_timezones.go

import (
	"context"
	"time"

	"foyer/internal/adapters"
)

// Adapter is a Stalker portal as the Adapters page shows it: Loge's fields
// for one, and what only a server shares — its lines. check is the service's
// own, made after the registry that lists this.
func Adapter(check func(context.Context, adapters.Settings) adapters.Result) adapters.Adapter {
	return adapters.Adapter{
		Key:      Key,
		ID:       "iptv/stalker",
		Category: adapters.IPTV,
		Name:     "Stalker portal",
		Description: "A Stalker or Ministra portal, reached as the one box its MAC address is: Foyer becomes the portal's only client, " +
			"reads its channels once in a while, keeps its guide, and shares its streams among the household's devices — one stream for a channel " +
			"watched on several. Its films and series come later.",
		Fields: []adapters.Field{
			{Key: "portalUrl", Label: "Portal address", Type: adapters.URL, Required: true, Help: "As the provider gives it, such as http://portal.example.com/c/."},
			{Key: "mac", Label: "MAC address", Type: adapters.Password, Visible: true, Required: true, Help: "The one the provider registered. It signs in, so keep it as you would a password."},
			{Key: "serialNumber", Label: "Serial number", Type: adapters.Password, Visible: true, Help: "Only if your provider asks for one."},
			{Key: "deviceId", Label: "Device id", Type: adapters.Password, Visible: true, Help: "Only if your provider asks for one."},
			{Key: "signature", Label: "Signature", Type: adapters.Password, Visible: true, Help: "Only if your provider asks for one."},
			{Key: "streams", Label: "Streams at once", Type: adapters.Number, Default: 1, Min: new(1), Max: new(4),
				Help: "As many as the subscription allows. The same channel on several devices is one stream; a device that finds them all in use is shown who watches what, and may take one over."},
			{Key: "forced", Label: "Can be forced", Type: adapters.Bool, Default: false,
				Help: "Lets a device that finds every stream in use open one more, for as long as it plays. The provider may end another stream for it."},
			{Key: "requestsAtOnce", Label: "Requests at once", Type: adapters.Number, Default: 1, Min: new(1), Max: new(4),
				Help: "How many requests may be on their way to the portal at once, a quarter of a second apart. One is gentlest: a portal that counts requests can block an address that asks too fast. Starting a channel always goes first."},
			{Key: "timeZone", Label: "Guide time zone", Type: adapters.Select, Default: "", Options: timeZones,
				Help: "As the portal says, unless what is on now is off by an hour or more: then the time zone the portal keeps its guide in — often its own, such as Europe/Berlin."},
			{Key: "guideByCountry", Label: "Put other countries’ guides right", Type: adapters.Bool, Default: true,
				Help: "A channel from another country than the portal's own can have a guide written in UTC that the portal takes for its own clock, hours early. Such a guide is read as UTC."},
			{Key: "lineupDays", Label: "Read the channels again every (days)", Type: adapters.Number, Default: 7, Min: new(1), Max: new(30),
				Help: "Foyer keeps the portal's channels and reads them again this often, or when someone pulls to refresh. A portal counts requests."},
		},
		Test: func(ctx context.Context, settings adapters.Settings) adapters.Result {
			ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
			defer cancel()
			return check(ctx, settings)
		},
	}
}
