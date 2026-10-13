package stalker

// countryZones is one zone per country (ISO 3166-1 alpha-2), the one most of
// its viewers keep: `COUNTRY_ZONES` in the app's `@loge/api`, held to the
// same answers by the shared countries.json.
var countryZones = map[string]string{
	"AD": "Europe/Andorra", "AE": "Asia/Dubai", "AF": "Asia/Kabul", "AG": "America/Antigua", "AI": "America/Anguilla",
	"AL": "Europe/Tirane", "AM": "Asia/Yerevan", "AO": "Africa/Luanda", "AR": "America/Buenos_Aires", "AS": "Pacific/Pago_Pago",
	"AT": "Europe/Vienna", "AU": "Australia/Sydney", "AW": "America/Aruba", "AX": "Europe/Mariehamn", "AZ": "Asia/Baku",
	"BA": "Europe/Sarajevo", "BB": "America/Barbados", "BD": "Asia/Dhaka", "BE": "Europe/Brussels", "BF": "Africa/Ouagadougou",
	"BG": "Europe/Sofia", "BH": "Asia/Bahrain", "BI": "Africa/Bujumbura", "BJ": "Africa/Porto-Novo", "BL": "America/St_Barthelemy",
	"BM": "Atlantic/Bermuda", "BN": "Asia/Brunei", "BO": "America/La_Paz", "BQ": "America/Kralendijk", "BR": "America/Sao_Paulo",
	"BS": "America/Nassau", "BT": "Asia/Thimphu", "BW": "Africa/Gaborone", "BY": "Europe/Minsk", "BZ": "America/Belize",
	"CA": "America/Toronto", "CC": "Indian/Cocos", "CD": "Africa/Kinshasa", "CF": "Africa/Bangui", "CG": "Africa/Brazzaville",
	"CH": "Europe/Zurich", "CI": "Africa/Abidjan", "CK": "Pacific/Rarotonga", "CL": "America/Santiago", "CM": "Africa/Douala",
	"CN": "Asia/Shanghai", "CO": "America/Bogota", "CR": "America/Costa_Rica", "CU": "America/Havana", "CV": "Atlantic/Cape_Verde",
	"CW": "America/Curacao", "CX": "Indian/Christmas", "CY": "Asia/Nicosia", "CZ": "Europe/Prague", "DE": "Europe/Berlin",
	"DJ": "Africa/Djibouti", "DK": "Europe/Copenhagen", "DM": "America/Dominica", "DO": "America/Santo_Domingo", "DZ": "Africa/Algiers",
	"EC": "America/Guayaquil", "EE": "Europe/Tallinn", "EG": "Africa/Cairo", "EH": "Africa/El_Aaiun", "ER": "Africa/Asmera",
	"ES": "Europe/Madrid", "ET": "Africa/Addis_Ababa", "FI": "Europe/Helsinki", "FJ": "Pacific/Fiji", "FK": "Atlantic/Stanley",
	"FM": "Pacific/Ponape", "FO": "Atlantic/Faeroe", "FR": "Europe/Paris", "GA": "Africa/Libreville", "GB": "Europe/London",
	"GD": "America/Grenada", "GE": "Asia/Tbilisi", "GF": "America/Cayenne", "GG": "Europe/Guernsey", "GH": "Africa/Accra",
	"GI": "Europe/Gibraltar", "GL": "America/Godthab", "GM": "Africa/Banjul", "GN": "Africa/Conakry", "GP": "America/Guadeloupe",
	"GQ": "Africa/Malabo", "GR": "Europe/Athens", "GS": "Atlantic/South_Georgia", "GT": "America/Guatemala", "GU": "Pacific/Guam",
	"GW": "Africa/Bissau", "GY": "America/Guyana", "HK": "Asia/Hong_Kong", "HN": "America/Tegucigalpa", "HR": "Europe/Zagreb",
	"HT": "America/Port-au-Prince", "HU": "Europe/Budapest", "ID": "Asia/Jakarta", "IE": "Europe/Dublin", "IL": "Asia/Jerusalem",
	"IM": "Europe/Isle_of_Man", "IN": "Asia/Calcutta", "IO": "Indian/Chagos", "IQ": "Asia/Baghdad", "IR": "Asia/Tehran",
	"IS": "Atlantic/Reykjavik", "IT": "Europe/Rome", "JE": "Europe/Jersey", "JM": "America/Jamaica", "JO": "Asia/Amman",
	"JP": "Asia/Tokyo", "KE": "Africa/Nairobi", "KG": "Asia/Bishkek", "KH": "Asia/Phnom_Penh", "KI": "Pacific/Tarawa",
	"KM": "Indian/Comoro", "KN": "America/St_Kitts", "KP": "Asia/Pyongyang", "KR": "Asia/Seoul", "KW": "Asia/Kuwait",
	"KY": "America/Cayman", "KZ": "Asia/Almaty", "LA": "Asia/Vientiane", "LB": "Asia/Beirut", "LC": "America/St_Lucia",
	"LI": "Europe/Vaduz", "LK": "Asia/Colombo", "LR": "Africa/Monrovia", "LS": "Africa/Maseru", "LT": "Europe/Vilnius",
	"LU": "Europe/Luxembourg", "LV": "Europe/Riga", "LY": "Africa/Tripoli", "MA": "Africa/Casablanca", "MC": "Europe/Monaco",
	"MD": "Europe/Chisinau", "ME": "Europe/Podgorica", "MF": "America/Marigot", "MG": "Indian/Antananarivo", "MH": "Pacific/Majuro",
	"MK": "Europe/Skopje", "ML": "Africa/Bamako", "MM": "Asia/Rangoon", "MN": "Asia/Ulaanbaatar", "MO": "Asia/Macau",
	"MP": "Pacific/Saipan", "MQ": "America/Martinique", "MR": "Africa/Nouakchott", "MS": "America/Montserrat", "MT": "Europe/Malta",
	"MU": "Indian/Mauritius", "MV": "Indian/Maldives", "MW": "Africa/Blantyre", "MX": "America/Mexico_City", "MY": "Asia/Kuala_Lumpur",
	"MZ": "Africa/Maputo", "NA": "Africa/Windhoek", "NC": "Pacific/Noumea", "NE": "Africa/Niamey", "NF": "Pacific/Norfolk",
	"NG": "Africa/Lagos", "NI": "America/Managua", "NL": "Europe/Amsterdam", "NO": "Europe/Oslo", "NP": "Asia/Katmandu",
	"NR": "Pacific/Nauru", "NU": "Pacific/Niue", "NZ": "Pacific/Auckland", "OM": "Asia/Muscat", "PA": "America/Panama",
	"PE": "America/Lima", "PF": "Pacific/Tahiti", "PG": "Pacific/Port_Moresby", "PH": "Asia/Manila", "PK": "Asia/Karachi",
	"PL": "Europe/Warsaw", "PM": "America/Miquelon", "PN": "Pacific/Pitcairn", "PR": "America/Puerto_Rico", "PS": "Asia/Gaza",
	"PT": "Europe/Lisbon", "PW": "Pacific/Palau", "PY": "America/Asuncion", "QA": "Asia/Qatar", "RE": "Indian/Reunion",
	"RO": "Europe/Bucharest", "RS": "Europe/Belgrade", "RU": "Europe/Moscow", "RW": "Africa/Kigali", "SA": "Asia/Riyadh",
	"SB": "Pacific/Guadalcanal", "SC": "Indian/Mahe", "SD": "Africa/Khartoum", "SE": "Europe/Stockholm", "SG": "Asia/Singapore",
	"SH": "Atlantic/St_Helena", "SI": "Europe/Ljubljana", "SJ": "Arctic/Longyearbyen", "SK": "Europe/Bratislava", "SL": "Africa/Freetown",
	"SM": "Europe/San_Marino", "SN": "Africa/Dakar", "SO": "Africa/Mogadishu", "SR": "America/Paramaribo", "SS": "Africa/Juba",
	"ST": "Africa/Sao_Tome", "SV": "America/El_Salvador", "SX": "America/Lower_Princes", "SY": "Asia/Damascus", "SZ": "Africa/Mbabane",
	"TC": "America/Grand_Turk", "TD": "Africa/Ndjamena", "TF": "Indian/Kerguelen", "TG": "Africa/Lome", "TH": "Asia/Bangkok",
	"TJ": "Asia/Dushanbe", "TK": "Pacific/Fakaofo", "TL": "Asia/Dili", "TM": "Asia/Ashgabat", "TN": "Africa/Tunis",
	"TO": "Pacific/Tongatapu", "TR": "Europe/Istanbul", "TT": "America/Port_of_Spain", "TV": "Pacific/Funafuti", "TW": "Asia/Taipei",
	"TZ": "Africa/Dar_es_Salaam", "UA": "Europe/Kiev", "UG": "Africa/Kampala", "UM": "Pacific/Wake", "US": "America/New_York",
	"UY": "America/Montevideo", "UZ": "Asia/Tashkent", "VA": "Europe/Vatican", "VC": "America/St_Vincent", "VE": "America/Caracas",
	"VG": "America/Tortola", "VI": "America/St_Thomas", "VN": "Asia/Saigon", "VU": "Pacific/Efate", "WF": "Pacific/Wallis",
	"WS": "Pacific/Apia", "YE": "Asia/Aden", "YT": "Indian/Mayotte", "ZA": "Africa/Johannesburg", "ZM": "Africa/Lusaka",
	"ZW": "Africa/Harare",
}

// countryNames are names a group or a channel may start with instead of a
// code: in English, in the country's own words, and as IPTV lists spell them.
var countryNames = map[string]string{
	"GERMANY": "DE", "DEUTSCHLAND": "DE", "ALMANYA": "DE", "TURKEY": "TR",
	"TURKIYE": "TR", "TÜRKIYE": "TR", "TÜRKİYE": "TR", "CYPRUS": "CY",
	"KIBRIS": "CY", "AUSTRIA": "AT", "ÖSTERREICH": "AT", "OSTERREICH": "AT",
	"SWITZERLAND": "CH", "SCHWEIZ": "CH", "SUISSE": "CH", "NETHERLANDS": "NL",
	"NEDERLAND": "NL", "HOLLAND": "NL", "BELGIUM": "BE", "BELGIE": "BE",
	"BELGIQUE": "BE", "FRANCE": "FR", "UNITED KINGDOM": "GB", "ENGLAND": "GB",
	"BRITAIN": "GB", "SCOTLAND": "GB", "WALES": "GB", "IRELAND": "IE",
	"ITALY": "IT", "ITALIA": "IT", "SPAIN": "ES", "ESPANA": "ES",
	"ESPAÑA": "ES", "PORTUGAL": "PT", "POLAND": "PL", "POLSKA": "PL",
	"CZECHIA": "CZ", "CZECH": "CZ", "SLOVAKIA": "SK", "HUNGARY": "HU",
	"MAGYAR": "HU", "ROMANIA": "RO", "BULGARIA": "BG", "GREECE": "GR",
	"ELLADA": "GR", "ALBANIA": "AL", "SHQIPERI": "AL", "SHQIPERIA": "AL",
	"SERBIA": "RS", "SRBIJA": "RS", "CROATIA": "HR", "HRVATSKA": "HR",
	"BOSNIA": "BA", "MACEDONIA": "MK", "SLOVENIA": "SI", "RUSSIA": "RU",
	"UKRAINE": "UA", "SWEDEN": "SE", "SVERIGE": "SE", "NORWAY": "NO",
	"NORGE": "NO", "DENMARK": "DK", "DANMARK": "DK", "FINLAND": "FI",
	"SUOMI": "FI", "UNITED STATES": "US", "AMERICA": "US", "CANADA": "CA",
	"MEXICO": "MX", "BRAZIL": "BR", "BRASIL": "BR", "ARGENTINA": "AR",
	"INDIA": "IN", "PAKISTAN": "PK", "BANGLADESH": "BD", "AFGHANISTAN": "AF",
	"IRAN": "IR", "IRAQ": "IQ", "SYRIA": "SY", "LEBANON": "LB",
	"LIBAN": "LB", "JORDAN": "JO", "PALESTINE": "PS", "PALASTINE": "PS",
	"ISRAEL": "IL", "SAUDI ARABIA": "SA", "SAUDI": "SA", "UNITED ARAB EMIRATES": "AE",
	"EMIRATES": "AE", "QATAR": "QA", "KUWAIT": "KW", "BAHRAIN": "BH",
	"OMAN": "OM", "YEMEN": "YE", "EGYPT": "EG", "LIBYA": "LY",
	"TUNISIA": "TN", "TUNISIE": "TN", "ALGERIA": "DZ", "ALGERIE": "DZ",
	"MOROCCO": "MA", "MAROC": "MA", "SUDAN": "SD", "AZERBAIJAN": "AZ",
	"ARMENIA": "AM", "KAZAKHSTAN": "KZ", "CHINA": "CN", "JAPAN": "JP",
	"KOREA": "KR", "PHILIPPINES": "PH", "VIETNAM": "VN", "THAILAND": "TH",
	"INDONESIA": "ID", "MALAYSIA": "MY", "AUSTRALIA": "AU", "NEW ZEALAND": "NZ",
	"SOUTH AFRICA": "ZA", "NIGERIA": "NG", "KENYA": "KE", "ETHIOPIA": "ET",
	"SOMALIA": "SO",
}

// countryAliases are codes that are no ISO code, or one IPTV lists use for
// something else.
var countryAliases = map[string]string{
	"UK": "GB", "USA": "US", "GER": "DE", "DEU": "DE", "TUR": "TR",
	"KKTC": "CY", "KSA": "SA", "UAE": "AE", "BIH": "BA",
}

// notCountries are codes that are a language (AR is Arabic, not Argentina),
// a channel's own words (TV, FM, LA) or nothing of a place.
var notCountries = map[string]bool{
	"AR": true, "EN": true, "TV": true, "FM": true, "LA": true, "HD": true, "SD": true, "FHD": true, "UHD": true, "VIP": true, "EX": true, "YU": true, "XXX": true, "ALL": true, "VOD": true, "PPV": true,
}
